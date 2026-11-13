//go:build darwin

// HTML to PDF with the Mac's own WebKit. See webpdf_darwin.go.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include "render.h"

extern void banyanRenderDone(uintptr_t handle, char *err);

@interface BanyanPDFJob : NSObject <WKNavigationDelegate>
@property(strong) WKWebView *web;
@property(strong) NSWindow *window;
@property(strong) NSURL *out;
@property CGFloat wPt;
@property CGFloat hPt;
@property(copy) void (^done)(NSString *err);
@property BOOL finished;
@property BOOL paged;
@property CGFloat mTop;
@property CGFloat mRight;
@property CGFloat mBottom;
@property CGFloat mLeft;
@end

@implementation BanyanPDFJob
- (void)finish:(NSString *)err {
  if (self.finished) return;
  self.finished = YES;
  self.web.navigationDelegate = nil;
  [self.window orderOut:nil];
  self.done(err);
}
- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)n withError:(NSError *)error {
  [self finish:[NSString stringWithFormat:@"the print page did not load: %@", error.localizedDescription]];
}
- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)n withError:(NSError *)error {
  [self finish:[NSString stringWithFormat:@"the print page did not load: %@", error.localizedDescription]];
}
- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)n {
  // Let fonts load and the layout script finish before the page is printed.
  NSString *settle = @"await document.fonts.ready; await new Promise(r => setTimeout(r, 60)); return true;";
  [webView callAsyncJavaScript:settle arguments:nil inFrame:nil inContentWorld:WKContentWorld.pageWorld
             completionHandler:^(id result, NSError *error) {
    if (error) { [self finish:[NSString stringWithFormat:@"the print page did not settle: %@", error.localizedDescription]]; return; }
    if (self.paged) [self print]; else [self snapshot];
  }];
}
// One page drawn at exactly 1 CSS px = 1/96 inch: the view is captured as
// vector PDF (1 px = 1 pt) and redrawn at 0.75 onto a page of the exact size.
- (void)snapshot {
  WKPDFConfiguration *cfg = [WKPDFConfiguration new];
  cfg.rect = self.web.bounds;
  [self.web createPDFWithConfiguration:cfg completionHandler:^(NSData *data, NSError *error) {
    if (error || !data) { [self finish:[NSString stringWithFormat:@"the page could not be captured: %@", error.localizedDescription]]; return; }
    CGDataProviderRef provider = CGDataProviderCreateWithCFData((__bridge CFDataRef)data);
    CGPDFDocumentRef doc = CGPDFDocumentCreateWithProvider(provider);
    CGDataProviderRelease(provider);
    if (!doc || CGPDFDocumentGetNumberOfPages(doc) != 1) {
      if (doc) CGPDFDocumentRelease(doc);
      [self finish:@"the captured page was not a single page"]; return;
    }
    CGPDFPageRef page = CGPDFDocumentGetPage(doc, 1);
    CGRect src = CGPDFPageGetBoxRect(page, kCGPDFMediaBox);
    CGRect box = CGRectMake(0, 0, self.wPt, self.hPt);
    CGContextRef ctx = CGPDFContextCreateWithURL((__bridge CFURLRef)self.out, &box, NULL);
    if (!ctx) { CGPDFDocumentRelease(doc); [self finish:@"the PDF could not be written"]; return; }
    CGPDFContextBeginPage(ctx, NULL);
    // Top-left corners coincide; PDF's origin is bottom-left.
    CGContextTranslateCTM(ctx, 0, self.hPt - src.size.height * 0.75);
    CGContextScaleCTM(ctx, 0.75, 0.75);
    CGContextTranslateCTM(ctx, -src.origin.x, -src.origin.y);
    CGContextDrawPDFPage(ctx, page);
    CGPDFContextEndPage(ctx);
    CGPDFContextClose(ctx);
    CGContextRelease(ctx);
    CGPDFDocumentRelease(doc);
    [self finish:nil];
  }];
}
- (void)print {
  NSPrintInfo *info = [[NSPrintInfo alloc] initWithDictionary:@{
    NSPrintJobDisposition : NSPrintSaveJob,
    NSPrintJobSavingURL : self.out,
  }];
  info.paperSize = NSMakeSize(self.wPt, self.hPt);
  info.orientation = NSPaperOrientationPortrait;
  info.topMargin = self.mTop; info.bottomMargin = self.mBottom; info.leftMargin = self.mLeft; info.rightMargin = self.mRight;
  info.horizontallyCentered = NO; info.verticallyCentered = NO;
  info.scalingFactor = 1.0;
  info.horizontalPagination = NSPrintingPaginationModeAutomatic;
  info.verticalPagination = NSPrintingPaginationModeAutomatic;
  NSPrintOperation *op = [self.web printOperationWithPrintInfo:info];
  op.showsPrintPanel = NO;
  op.showsProgressPanel = NO;
  op.view.frame = NSMakeRect(0, 0, self.wPt, self.hPt);
  [op runOperationModalForWindow:self.window delegate:self
                  didRunSelector:@selector(printOperationDidRun:success:contextInfo:) contextInfo:NULL];
}
- (void)printOperationDidRun:(NSPrintOperation *)op success:(BOOL)success contextInfo:(void *)ctx {
  [self finish:success ? nil : @"the page could not be written as a PDF"];
}
@end

static BanyanPDFJob *startJob(NSString *html, NSString *pdf, double wMM, double hMM, BOOL paged, const double *marginsMM, void (^done)(NSString *)) {
  BanyanPDFJob *job = [BanyanPDFJob new];
  job.wPt = wMM / 25.4 * 72.0;
  job.hPt = hMM / 25.4 * 72.0;
  job.out = [NSURL fileURLWithPath:pdf];
  job.done = done;
  job.paged = paged;
  job.mTop = marginsMM[0] / 25.4 * 72.0; job.mRight = marginsMM[1] / 25.4 * 72.0;
  job.mBottom = marginsMM[2] / 25.4 * 72.0; job.mLeft = marginsMM[3] / 25.4 * 72.0;
  // A single page is laid out in CSS px (96 to the inch), rounded up to whole px.
  NSRect frame = paged ? NSMakeRect(0, 0, job.wPt, job.hPt)
                       : NSMakeRect(0, 0, ceil(wMM / 25.4 * 96.0), ceil(hMM / 25.4 * 96.0));
  WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
  cfg.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
  job.web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
  job.web.navigationDelegate = job;
  job.window = [[NSWindow alloc] initWithContentRect:frame styleMask:NSWindowStyleMaskBorderless
                                             backing:NSBackingStoreBuffered defer:YES];
  job.window.releasedWhenClosed = NO;
  job.window.contentView = job.web;
  NSURL *url = [NSURL fileURLWithPath:html];
  [job.web loadFileURL:url allowingReadAccessToURL:[url URLByDeletingLastPathComponent]];
  return job;
}

void banyanPumpMainLoop(double seconds) {
  @autoreleasepool {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
      [NSApplication sharedApplication];
      [NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
    });
    [[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:seconds]];
  }
}

// Jobs in flight, so nothing is released before its page is written.
static NSMutableSet<BanyanPDFJob *> *liveJobs(void) {
  static NSMutableSet *jobs;
  static dispatch_once_t once;
  dispatch_once(&once, ^{ jobs = [NSMutableSet new]; });
  return jobs;
}

void banyanRenderAsync(const char *htmlPath, const char *pdfPath, double wMM, double hMM, int paged, const double *marginsMM, uintptr_t handle) {
  NSString *html = @(htmlPath);
  NSString *pdf = @(pdfPath);
  double m[4] = {marginsMM[0], marginsMM[1], marginsMM[2], marginsMM[3]};
  double *margins = malloc(sizeof m);
  memcpy(margins, m, sizeof m);
  dispatch_async(dispatch_get_main_queue(), ^{
    __block BanyanPDFJob *job = nil;
    job = startJob(html, pdf, wMM, hMM, paged != 0, margins, ^(NSString *err) {
      banyanRenderDone(handle, err ? strdup(err.UTF8String) : NULL);
      dispatch_async(dispatch_get_main_queue(), ^{ if (job) [liveJobs() removeObject:job]; });
    });
    free(margins);
    [liveJobs() addObject:job];
  });
}
