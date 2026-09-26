#!/data/data/com.termux/files/usr/bin/env python3
"""合成 TavernLab 回复提示音（chime），零资产、零依赖。

参数刻意与 Flutter App 内 lib/main.dart 的 _synthChime() 保持一致：
两个正弦音 A5(880Hz) -> D6(1174.66Hz)，22050Hz / 16bit / 单声道，
指数衰减包络 exp(-age * 5.5)。这样手机脚本和 App 前台提示音听起来是同一个。

用法:
    gen-chime.py [输出路径] [--notes 880,1174.66] [--lens 0.20,0.38]
"""

import argparse
import math
import os
import struct
import sys


def synth(notes, lens, rate=22050, decay=5.5, amp=28000):
    n = int(sum(lens) * rate)
    out = bytearray()
    t = 0.0
    ni = 0
    note_start = 0.0
    for i in range(n):
        t = i / rate
        while ni < len(notes) - 1 and t >= note_start + lens[ni]:
            note_start += lens[ni]
            ni += 1
        f = notes[ni]
        age = t - note_start
        env = math.exp(-age * decay)
        v = int(math.sin(2 * math.pi * f * age) * env * amp)
        out += struct.pack("<h", max(-32768, min(32767, v)))
    return bytes(out)


def wav(data, rate):
    hdr = (
        b"RIFF"
        + struct.pack("<I", 36 + len(data))
        + b"WAVEfmt "
        + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16)
        + b"data"
        + struct.pack("<I", len(data))
    )
    return hdr + data


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out", nargs="?", default=None, help="输出 .wav 路径")
    ap.add_argument("--notes", default="880,1174.66", help="各音频率 Hz，逗号分隔")
    ap.add_argument("--lens", default="0.20,0.38", help="各音时长 秒，逗号分隔")
    ap.add_argument("--rate", type=int, default=22050)
    ap.add_argument("--decay", type=float, default=5.5)
    ap.add_argument("--amp", type=int, default=28000)
    a = ap.parse_args()

    notes = [float(x) for x in a.notes.split(",")]
    lens = [float(x) for x in a.lens.split(",")]
    if len(notes) != len(lens) or not notes:
        sys.exit("notes 与 lens 数量必须一致且非空")

    out = a.out
    if out is None:
        prefix = os.environ.get("PREFIX", os.path.expanduser("~/.termux"))
        out = os.path.join(prefix, "share", "tl-chime.wav")
    os.makedirs(os.path.dirname(out), exist_ok=True)

    with open(out, "wb") as f:
        f.write(wav(synth(notes, lens, a.rate, a.decay, a.amp), a.rate))
    print(f"chime -> {out} ({os.path.getsize(out)} bytes, {sum(lens):.2f}s)")


if __name__ == "__main__":
    main()
