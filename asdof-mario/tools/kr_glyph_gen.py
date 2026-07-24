#!/usr/bin/env python3
# 한글 음절을 SM64 대사 폰트 포맷(IA1, 8x16 또는 16x16, 1bit/px)의 C 배열(.inc.c)로 변환.
# 사용: kr_glyph_gen.py "안" out.inc.c [width] [height]
import sys
from PIL import Image, ImageFont, ImageDraw

FONT = "/System/Library/Fonts/AppleSDGothicNeo.ttc"

def gen(ch, w, h):
    # 약간 크게 렌더 후 셀에 맞춤 (1bit 임계값)
    img = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(img)
    # 폰트 크기: 셀 높이에 맞춤
    fsz = h
    for try_sz in range(h, 6, -1):
        f = ImageFont.truetype(FONT, try_sz, index=0)
        bb = d.textbbox((0, 0), ch, font=f)
        tw, th = bb[2]-bb[0], bb[3]-bb[1]
        if tw <= w and th <= h:
            fsz = try_sz
            break
    f = ImageFont.truetype(FONT, fsz, index=0)
    bb = d.textbbox((0, 0), ch, font=f)
    tw, th = bb[2]-bb[0], bb[3]-bb[1]
    ox = (w - tw)//2 - bb[0]
    oy = (h - th)//2 - bb[1]
    d.text((ox, oy), ch, fill=255, font=f)
    # 1bit 패킹 (행별 MSB=왼쪽), 임계값 128
    out = bytearray()
    px = img.load()
    for y in range(h):
        bit = 0; nbits = 0
        for x in range(w):
            bit = (bit << 1) | (1 if px[x, y] >= 128 else 0)
            nbits += 1
            if nbits == 8:
                out.append(bit); bit = 0; nbits = 0
        if nbits:  # w 가 8의 배수가 아니면 패딩
            out.append(bit << (8 - nbits))
    return out

def main():
    ch = sys.argv[1]
    outp = sys.argv[2]
    w = int(sys.argv[3]) if len(sys.argv) > 3 else 8
    h = int(sys.argv[4]) if len(sys.argv) > 4 else 16
    data = gen(ch, w, h)
    lines = ", ".join("0x%02X" % b for b in data)
    with open(outp, "w") as fp:
        fp.write("// auto-generated Korean glyph '%s' (%dx%d IA1, %d bytes)\n" % (ch, w, h, len(data)))
        fp.write(lines + "\n")
    print("wrote %s : %d bytes (%dx%d)" % (outp, len(data), w, h))

main()
