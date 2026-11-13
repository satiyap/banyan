#include <stdint.h>

// Renders on the main queue of a process whose main run loop is running, and
// reports through banyanRenderDone. marginsMM is top, right, bottom, left and
// applies to paged documents only.
void banyanRenderAsync(const char *htmlPath, const char *pdfPath, double wMM, double hMM, int paged, const double *marginsMM, uintptr_t handle);

// Runs the main run loop for a moment. For programs with no app of their own;
// call it on the main thread.
void banyanPumpMainLoop(double seconds);
