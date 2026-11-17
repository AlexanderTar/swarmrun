#!/usr/bin/env bash
# Rebuilds the app icon headlessly from the committed sources: renders
# Resources/AppIconLayers/Glyph.svg to Glyph.png, copies the layers into the
# Icon Composer document Resources/Swarm.icon, renders the real Liquid Glass icon
# with Xcode's ictool, and packs Resources/Swarm.icns (16-1024 px) plus
# AppIconLayers/Composed-1024.png (the full-bleed Icon Composer export). It also
# writes the same Liquid Glass icon everywhere else Swarm shows it: the design
# system's app-icon.png, the favicon (board, relay offline page, site) and the
# opaque apple-touch-icon (board, site), and re-inlines the board's touch icon.
# Needs Xcode (ictool ships inside Icon Composer.app), rsvg-convert (brew install
# librsvg) and Pillow for python3 (brew install pillow).
set -euo pipefail

res="$(cd "$(dirname "$0")/../Resources" && pwd)"
layers="$res/AppIconLayers"
ictool="$(xcode-select -p)/../Applications/Icon Composer.app/Contents/Executables/ictool"
[ -x "$ictool" ] || { echo "ictool not found at $ictool" >&2; exit 1; }
command -v rsvg-convert >/dev/null || { echo "rsvg-convert not found (brew install librsvg)" >&2; exit 1; }
python3 -c 'import PIL' 2>/dev/null || { echo "Pillow not found for python3 (brew install pillow)" >&2; exit 1; }
root="$(cd "$res/../../.." && pwd)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

rsvg-convert -w 1024 -h 1024 "$layers/Glyph.svg" -o "$layers/Glyph.png"
cp "$layers/Background.png" "$layers/Glyph.png" "$res/Swarm.icon/Assets/"

render() { # <tile px> <output png>
  "$ictool" "$res/Swarm.icon" --export-image --output-file "$2" --platform macOS \
    --rendition Default --width "$1" --height "$1" --scale 1 >/dev/null
}

render 1024 "$layers/Composed-1024.png"

# icns art sits inside the standard macOS margins: an 824 px tile on a 1024 px
# canvas, rendered at its final size so the glass lighting stays crisp. The tile
# keeps the canvas parity so the margins are even.
iconset="$work/Swarm.iconset"
mkdir "$iconset"
for px in 16 32 64 128 256 512 1024; do
  tile=$(( (px * 824 + 512) / 1024 ))
  [ $(( (px - tile) % 2 )) -eq 0 ] || tile=$(( tile + 1 ))
  render "$tile" "$work/tile-$px.png"
  sips --padToHeightWidth "$px" "$px" "$work/tile-$px.png" --out "$work/icon-$px.png" >/dev/null
done
for spec in 16:16x16 32:16x16@2x 32:32x32 64:32x32@2x 128:128x128 256:128x128@2x \
            256:256x256 512:256x256@2x 512:512x512 1024:512x512@2x; do
  cp "$work/icon-${spec%%:*}.png" "$iconset/icon_${spec#*:}.png"
done
iconutil -c icns "$iconset" -o "$res/Swarm.icns"
echo "built $res/Swarm.icns"

# Web and design-system copies: the bare tile (no macOS margins, transparent
# corners), rendered at each final size so the glass stays crisp.
render 512 "$root/design/assets/Logos/app-icon.png"
render 96 "$work/favicon.png"
for dest in web/public site/assets internal/relay; do
  cp "$work/favicon.png" "$root/$dest/favicon.png"
done

# apple-touch-icon: iOS fills transparency with black and applies its own mask,
# so the 180 px tile sits on the opaque background layer and is saved as RGB.
render 180 "$work/touch-tile.png"
python3 -I - "$layers/Background.png" "$work/touch-tile.png" "$work/apple-touch-icon.png" <<'PY'
import sys
from PIL import Image
bg = Image.open(sys.argv[1]).convert("RGBA").resize((180, 180), Image.LANCZOS)
bg.alpha_composite(Image.open(sys.argv[2]).convert("RGBA"))
bg.convert("RGB").save(sys.argv[3], optimize=True)
PY
cp "$work/apple-touch-icon.png" "$root/web/public/apple-touch-icon.png"
cp "$work/apple-touch-icon.png" "$root/site/assets/apple-touch-icon.png"

# The board inlines its touch icon (iOS freezes a monogram if the fetch is slow).
python3 -I - "$root/web/index.html" "$work/apple-touch-icon.png" <<'PY'
import base64, re, sys
html = open(sys.argv[1]).read()
uri = "data:image/png;base64," + base64.b64encode(open(sys.argv[2], "rb").read()).decode()
html, n = re.subn(r'(rel="apple-touch-icon" href=")data:image/png;base64,[A-Za-z0-9+/=]+', lambda m: m.group(1) + uri, html)
assert n == 1, "web/index.html: apple-touch-icon link not found"
open(sys.argv[1], "w").write(html)
PY
# The design system's Logo card inlines a 2x render for its 96 px tile (the page
# may not reference files or uploads from a preview).
render 192 "$work/logo-card.png"
python3 -I - "$root/design/components/Logo/preview.html" "$work/logo-card.png" <<'PY'
import base64, re, sys
html = open(sys.argv[1]).read()
uri = "data:image/png;base64," + base64.b64encode(open(sys.argv[2], "rb").read()).decode()
html, n = re.subn(r'(<img class="app-icon"[^>]* src=")data:image/png;base64,[A-Za-z0-9+/=]*', lambda m: m.group(1) + uri, html)
assert n == 1, "Logo preview: app-icon img not found"
open(sys.argv[1], "w").write(html)
PY
echo "built web and design-system icons"
