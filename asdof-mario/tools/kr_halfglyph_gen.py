#!/usr/bin/env python3
# 한글 음절을 16x16 으로 렌더한 뒤 좌/우 8x16 두 개의 IA1 글리프(.ia1.inc.c)로 쪼갠다.
# SM64 대사 폰트(8x16 IA1)와 동일 포맷이라, LUT 슬롯에 넣으면 render_generic_char 로 그대로 렌더됨.
#   사용: kr_halfglyph_gen.py <neodgm.ttf> <outdir> "안녕..."
import sys, os
from PIL import Image, ImageFont, ImageDraw

def render_cell(ch, font):
    W, H = 16, 16
    img = Image.new("L", (W, H), 0)
    d = ImageDraw.Draw(img)
    bb = d.textbbox((0, 0), ch, font=font)
    # 좌상단 정렬 + 가로 중앙, y 는 폰트 기준선대로
    d.text((-bb[0], -bb[1]), ch, fill=255, font=font)
    return img

def ia1_bytes(img, x0):
    # img 의 x0..x0+7, 16행을 MSB=왼쪽으로 1bit 패킹 → 16바이트
    px = img.load()
    out = bytearray()
    for y in range(16):
        b = 0
        for i in range(8):
            b = (b << 1) | (1 if px[x0 + i, y] >= 128 else 0)
        out.append(b)
    return out

def write_inc(path, ch, half, data):
    with open(path, "w") as f:
        f.write("// '%s' %s half (8x16 IA1)\n" % (ch, half))
        f.write(", ".join("0x%02X" % b for b in data) + "\n")

def main():
    ttf, outdir, text = sys.argv[1], sys.argv[2], sys.argv[3]
    os.makedirs(outdir, exist_ok=True)
    font = ImageFont.truetype(ttf, 16)
    for ch in text:
        img = render_cell(ch, font)
        code = "%04X" % ord(ch)
        write_inc(os.path.join(outdir, "kr_%s_L.ia1.inc.c" % code), ch, "L", ia1_bytes(img, 0))
        write_inc(os.path.join(outdir, "kr_%s_R.ia1.inc.c" % code), ch, "R", ia1_bytes(img, 8))
        print("  %s U+%s → kr_%s_{L,R}.ia1.inc.c" % (ch, code, code))

main()
