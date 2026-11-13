/* Cheque field placement — the one implementation.
 *
 * Both the on-screen preview and the PDF that actually reaches the printer are
 * built by this file. That is the point of it: when preview and print are drawn
 * by two different code paths they drift, and the drift is only discovered on a
 * real cheque leaf.
 *
 * Coordinates are millimetres from the top-left corner of the landscape leaf
 * (204 x 92mm), before the 90-degree rotation onto the portrait printer page.
 * Text metrics are measured with canvas in the same engine that renders the
 * page, so wrapping and shrink-to-fit behave identically in both.
 */
(function (root) {
  'use strict';

  var CHQ_W = 204, CHQ_H = 92;
  var PAGE_W = 92, PAGE_H = 204;
  var HW_INSET = 4.23;          /* M126nw non-printable margin, all four edges */
  var MICR_TOP = 76;
  var PRINT_FONT = '"Helvetica Neue",Helvetica,Arial,sans-serif';

  var measureCtx = null;
  function ctx() {
    if (!measureCtx) measureCtx = document.createElement('canvas').getContext('2d');
    return measureCtx;
  }

  /* Measured at 10x the target size to keep sub-millimetre differences from
     being lost to integer rounding inside the text engine. */
  function textWidthMM(text, sizeMM, weight) {
    var c = ctx();
    c.font = (weight || 500) + ' ' + (sizeMM * 10) + 'px ' + PRINT_FONT;
    return c.measureText(text).width / 10;
  }

  function mm(v) { return (Math.round(v * 1000) / 1000) + 'mm'; }

  /* Fill trailing space with ' *' so no words can be added after the amount. */
  function starPad(text, maxW, size) {
    var out = text;
    while (textWidthMM(out + ' *', size) <= maxW) out += ' *';
    return out;
  }

  /* Greedy wrap across the two ruled lines, shrinking a step at a time until it
     fits. Returns overflow:true when even the smallest legible size will not do,
     so the caller can warn rather than print something unreadable. */
  function layoutWords(words, f1, f2, starFill) {
    var ladder = [f1.size, 3.1, 2.8, 2.5];
    for (var s = 0; s < ladder.length; s++) {
      var size = ladder[s];
      var tokens = words.split(' ');
      var line1 = '', i = 0;
      while (i < tokens.length) {
        var candidate = line1 ? line1 + ' ' + tokens[i] : tokens[i];
        if (textWidthMM(candidate, size) <= f1.w) { line1 = candidate; i++; } else break;
      }
      var line2 = tokens.slice(i).join(' ');
      if (line1 && textWidthMM(line2, size) <= f2.w) {
        if (starFill) {
          if (line2) line2 = starPad(line2, f2.w, size);
          else { line1 = starPad(line1, f1.w, size); line2 = starPad('', f2.w, size); }
        }
        return { line1: line1, line2: line2, size: size, overflow: false };
      }
    }
    return { line1: words, line2: '', size: ladder[ladder.length - 1], overflow: true };
  }

  function fitText(text, field) {
    var ladder = [field.size, 3.1, 2.8, 2.5];
    for (var i = 0; i < ladder.length; i++) {
      if (textWidthMM(text, ladder[i]) <= field.w) {
        return { size: ladder[i], overflow: false };
      }
    }
    return { size: ladder[ladder.length - 1], overflow: true };
  }

  function elem(tag, cls, parent) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (parent) parent.appendChild(e);
    return e;
  }

  /* Compute every printable string and its fitted size. Pure: no DOM writes, so
     the server-rendered values and the preview agree before anything is drawn. */
  function compose(cal, data) {
    var F = cal.fields;
    var warnings = [];
    var out = { date: '', payee: '', payeeSize: F.payee.size, words1: '', words2: '',
                wordsSize: F.words1.size, figures: '' };

    if (data.cheque_date) {
      var p = data.cheque_date.split('-');            /* YYYY-MM-DD */
      out.date = p[2] + p[1] + p[0];                  /* DDMMYYYY */
    }
    if (data.payee) {
      var fit = fitText(data.payee, F.payee);
      out.payee = data.payee;
      out.payeeSize = fit.size;
      if (fit.overflow) warnings.push('The payee name is wider than the Pay line and may overrun it.');
    }
    if (data.amount_words) {
      var lay = layoutWords(data.amount_words, F.words1, F.words2, !!data.star_fill);
      out.words1 = lay.line1;
      out.words2 = lay.line2;
      out.wordsSize = lay.size;
      if (lay.overflow) warnings.push('The amount in words is too long for the two ruled lines.');
    }
    if (data.amount_figures) {
      out.figures = (data.star_fill ? '**' : '') + data.amount_figures;
    }
    out.warnings = warnings;
    return out;
  }

  /* Draw the fields into a container. Returns the composed values so a caller
     can surface the same warnings the print path would produce. */
  function render(container, cal, data) {
    var F = cal.fields;
    var composed = compose(cal, data);

    container.innerHTML = '';
    var space = elem('div', 'cheque-space', container);
    space.style.width = mm(CHQ_W);
    space.style.height = mm(CHQ_H);

    /* Paper-fixed reference layer: never moves with the calibration nudge,
       because the leaf's own edges do not move. */
    var paper = elem('div', 'cheque-paper-layer', space);

    /* Everything that prints sits inside the nudged group. */
    var content = elem('div', 'cheque-content', space);
    content.style.transform = 'translate(' + mm(cal.dx || 0) + ',' + mm(cal.dy || 0) + ')';

    if (composed.date) {
      for (var i = 0; i < 8; i++) {
        var cx = F.date.x + i * F.date.pitch;
        var d = elem('div', 'pf pf-digit', content);
        d.style.left = mm(cx - 2.2);
        d.style.top = mm(F.date.y);
        d.style.width = mm(4.4);
        d.style.fontSize = mm(F.date.size);
        d.textContent = composed.date.charAt(i) || '';
      }
    }

    function place(cls, x, y, size, text, maxW) {
      var n = elem('div', 'pf ' + cls, content);
      n.style.left = mm(x);
      n.style.top = mm(y);
      n.style.fontSize = mm(size);
      if (maxW) n.style.maxWidth = mm(maxW);
      n.textContent = text;
      return n;
    }

    if (composed.payee) place('pf-payee', F.payee.x, F.payee.y, composed.payeeSize, composed.payee, F.payee.w);
    if (composed.words1) place('pf-w1', F.words1.x, F.words1.y, composed.wordsSize, composed.words1);
    if (composed.words2) place('pf-w2', F.words2.x, F.words2.y, composed.wordsSize, composed.words2);
    if (composed.figures) place('pf-amt', F.amount.x, F.amount.y, F.amount.size, composed.figures);

    /* A/C payee crossing. The box's top and bottom borders are the two rules, so
       its height IS the gap between them. It is anchored inside the printer's
       non-printable margin on purpose: the ends clip, the rules and caption land
       in the printable middle. */
    if (data.crossing) {
      var C = F.cross;
      var cross = elem('div', 'pf pf-cross', content);
      cross.style.left = mm(C.x);
      cross.style.top = mm(C.y);
      cross.style.width = mm(C.w != null ? C.w : 28);
      cross.style.height = mm(C.h != null ? C.h : 3.4);
      cross.style.fontSize = mm(C.size != null ? C.size : 1.5);
      cross.style.letterSpacing = mm(C.tracking != null ? C.tracking : 0.15);
      cross.style.transform = 'rotate(' + (C.angle != null ? C.angle : -45) + 'deg)';
      cross.style.transformOrigin = C.origin === 'bottom left' ? '0 100%' : '0 0';
      var caption = elem('span', '', cross);
      caption.textContent = 'A/C PAYEE ONLY';
      caption.style.transform = 'translateX(' + mm(C.textDx || 0) + ')';
    }

    if (data.strike_order) {
      var s = elem('div', 'pf pf-strike', content);
      s.style.left = mm(F.bearer.x);
      s.style.top = mm(F.bearer.y);
      s.style.width = mm(F.bearer.w);
    }

    if (data.test_pattern) {
      paper.appendChild(buildTestSVG(cal, false));
      content.appendChild(buildTestSVG(cal, true));
    }

    return composed;
  }

  function svgEl(inner, cls) {
    var wrap = document.createElement('div');
    wrap.className = cls;
    wrap.innerHTML = '<svg width="' + CHQ_W + 'mm" height="' + CHQ_H + 'mm" viewBox="0 0 ' +
      CHQ_W + ' ' + CHQ_H + '" xmlns="http://www.w3.org/2000/svg">' + inner + '</svg>';
    return wrap;
  }

  /* The diagnostic sheet is split in two so the calibration loop stays honest:
     the paper half (edges, grid, rulers) is fixed to the leaf, while the field
     half rides inside the nudged group. Nudge by 2mm and the boxes move with the
     text, which is the whole point of holding the sheet against a real cheque. */
  function buildTestSVG(cal, fieldsOnly) {
    var F = cal.fields, g = '', i;

    if (fieldsOnly) {
      var box = function (x, y, w, h, label) {
        return '<rect x="' + x + '" y="' + y + '" width="' + w + '" height="' + h +
          '" fill="none" stroke="#000" stroke-width="0.25" stroke-dasharray="1.5 1"/>' +
          '<text x="' + (x + 0.8) + '" y="' + (y - 1) + '" font-size="2" fill="#000">' + label + '</text>';
      };
      g += box(F.date.x - F.date.pitch / 2, F.date.y - 1, F.date.pitch * 8, F.date.size + 2, 'DATE');
      g += box(F.payee.x, F.payee.y - 0.8, F.payee.w, 5.4, 'PAYEE');
      g += box(F.words1.x, F.words1.y - 0.8, F.words1.w, 5.4, 'WORDS 1');
      g += box(F.words2.x, F.words2.y - 0.8, F.words2.w, 5.4, 'WORDS 2');
      g += box(F.amount.x, F.amount.y - 1, 40, 6.4, 'AMOUNT');
      return svgEl(g, 'testlayer-fields');
    }

    for (i = 10; i < CHQ_W; i += 10) {
      g += '<line x1="' + i + '" y1="0" x2="' + i + '" y2="' + CHQ_H +
        '" stroke="#888" stroke-width="' + (i % 50 ? 0.08 : 0.2) + '"/>';
      if (i % 50 === 0) g += '<text x="' + (i + 0.8) + '" y="3" font-size="2.4" fill="#555">' + i + '</text>';
    }
    for (i = 10; i < CHQ_H; i += 10) {
      g += '<line x1="0" y1="' + i + '" x2="' + CHQ_W + '" y2="' + i +
        '" stroke="#888" stroke-width="' + (i % 50 ? 0.08 : 0.2) + '"/>';
      if (i % 50 === 0) g += '<text x="1" y="' + (i - 0.8) + '" font-size="2.4" fill="#555">' + i + '</text>';
    }

    /* the leaf edge — expected to be clipped, drawn so the clipping is visible */
    g += '<rect x="0.2" y="0.2" width="' + (CHQ_W - 0.4) + '" height="' + (CHQ_H - 0.4) +
      '" fill="none" stroke="#000" stroke-width="0.3"/>';
    /* the hardware printable limit — nothing outside this can ever receive toner */
    g += '<rect x="' + HW_INSET + '" y="' + HW_INSET + '" width="' + (CHQ_W - HW_INSET * 2) +
      '" height="' + (CHQ_H - HW_INSET * 2) + '" fill="none" stroke="#b3382c" ' +
      'stroke-width="0.25" stroke-dasharray="1 1"/>' +
      '<text x="' + (HW_INSET + 1) + '" y="' + (HW_INSET + 3) + '" font-size="2.2" fill="#b3382c">' +
      'printable limit 4.23mm — the outer solid line will clip</text>';

    /* 8mm inset reference box with ticks spanning the gap to the edge */
    var INSET = 8, iw = CHQ_W - INSET * 2, ih = CHQ_H - INSET * 2;
    g += '<rect x="' + INSET + '" y="' + INSET + '" width="' + iw + '" height="' + ih +
      '" fill="none" stroke="#000" stroke-width="0.3" stroke-dasharray="2 1.2"/>';
    [[0, 0], [CHQ_W, 0], [0, CHQ_H], [CHQ_W, CHQ_H]].forEach(function (c) {
      var ix = c[0] === 0 ? INSET : CHQ_W - INSET, iy = c[1] === 0 ? INSET : CHQ_H - INSET;
      g += '<line x1="' + c[0] + '" y1="' + iy + '" x2="' + ix + '" y2="' + iy +
        '" stroke="#b3382c" stroke-width="0.25"/>' +
        '<line x1="' + ix + '" y1="' + c[1] + '" x2="' + ix + '" y2="' + iy +
        '" stroke="#b3382c" stroke-width="0.25"/>';
    });
    g += '<text x="' + (INSET + 1) + '" y="' + (CHQ_H - INSET - 2) + '" font-size="2.4" fill="#000">' +
      '8mm inset box — ' + iw.toFixed(1) + ' x ' + ih.toFixed(1) + 'mm (red ticks = the 8mm gap)</text>';

    /* an exactly 100mm line: the only reliable way to catch a scale error */
    var lx = INSET, ly = 4.5;
    g += '<line x1="' + lx + '" y1="' + ly + '" x2="' + (lx + 100) + '" y2="' + ly +
      '" stroke="#0a5aa4" stroke-width="0.3"/>';
    for (i = 0; i <= 100; i += 10) {
      var t = i % 50 ? 0.7 : 1.2;
      g += '<line x1="' + (lx + i) + '" y1="' + (ly - t) + '" x2="' + (lx + i) + '" y2="' + (ly + t) +
        '" stroke="#0a5aa4" stroke-width="0.25"/>';
    }
    g += '<text x="' + lx + '" y="' + (ly - 1.8) + '" font-size="2.4" fill="#0a5aa4">' +
      '100.0mm reference — measure this with a ruler</text>';

    g += '<text x="4" y="52" font-size="3.4" fill="#000">TOP EDGE — date-box side</text>' +
      '<text x="4" y="58" font-size="2.6" fill="#000">Hold against a real leaf up to the light, then nudge.</text>' +
      '<line x1="0" y1="' + MICR_TOP + '" x2="' + CHQ_W + '" y2="' + MICR_TOP +
      '" stroke="#000" stroke-width="0.25" stroke-dasharray="3 2"/>' +
      '<text x="80" y="80" font-size="2.4" fill="#000">MICR BAND — never printed on</text>';
    return svgEl(g, 'testlayer-paper');
  }

  /* Rotation of the landscape leaf onto the portrait printer page.
   *   normal : (0,0)->(92,0)  (204,0)->(92,204)  (0,92)->(0,0)  (204,92)->(0,204)
   *   flipped: the same page covered from the opposite feed direction.
   */
  function printTransform(flip) {
    return flip
      ? 'translate(0mm,' + PAGE_H + 'mm) rotate(-90deg)'
      : 'translate(' + PAGE_H_EDGE() + 'mm,0mm) rotate(90deg)';
  }
  function PAGE_H_EDGE() { return CHQ_H; }   /* 92mm: the page's full width */

  root.ChequeLayout = {
    CHQ_W: CHQ_W, CHQ_H: CHQ_H, PAGE_W: PAGE_W, PAGE_H: PAGE_H,
    HW_INSET: HW_INSET, MICR_TOP: MICR_TOP,
    textWidthMM: textWidthMM, layoutWords: layoutWords, fitText: fitText,
    starPad: starPad, compose: compose, render: render,
    printTransform: printTransform, mm: mm
  };
})(window);
