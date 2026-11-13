<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>cheque</title>
<style>
/* The declared page is the leaf fed sideways: 92mm across, 204mm in the feed
   direction. Zero margin defines the coordinate system; it does not promise
   toner at the physical edges — the printer keeps its own 4.23mm inset. */
$page_css

html, body { margin: 0; padding: 0; background: #fff; }
#root { position: relative; width: $root_w; height: $root_h; overflow: hidden; }
#sheet { position: absolute; left: $sheet_x; top: $sheet_y;
         width: ${page_w}mm; height: ${page_h}mm; overflow: hidden; }

/* The landscape leaf rotated onto the portrait page. transform-origin is the
   page's top-left, so the translate values below are exact corner mappings. */
#sheet .cheque-space { position: absolute; left: 0; top: 0;
                       transform-origin: 0 0; overflow: visible; }

$cheque_css
</style>
</head>
<body>
<div id="root"><div id="sheet"><div id="mount"></div></div></div>

<script>
$layout_js
</script>
<script>
/* Data is inlined rather than fetched: this page is opened from a temporary
   file, and a fetch under file:// would be blocked, silently printing defaults. */
var DATA = $data;
var mount = document.getElementById('mount');
var composed = ChequeLayout.render(mount, DATA.cal, DATA.cheque);
var space = mount.querySelector('.cheque-space');
space.style.transform = ChequeLayout.printTransform(!!DATA.cal.flip);
/* Read back by the renderer to confirm what was actually composed. */
window.__composed = composed;
document.documentElement.setAttribute('data-ready', '1');
</script>
</body>
</html>
