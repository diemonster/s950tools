# Akai s950 Conversion Script

This script is a highly opinionated workflow for dealing with Akai s950 samples from MacOS.
[This Youtube Video](https://youtu.be/FRzrxnW3RL4?si=6HKeyiFVw6H32jCJ) inspired the workflow.

## Requirements

- MacOS (tested on Sonoma)
- [Awave Studio 12.5](https://www.fmjsoft.com/awavestudio.html#main)
- Akai s950
- HxC Floppy Emulator with SD Card
- [optional] Renoise or some way to create SFZ multisamples

## Instructions

- Create a SFZ file (with associated samples) in Renoise
- Open the SFZ file in Awave Studio and export it as a akai s950 `.P9` program
- Take the exported P9 and S9 files from export and place them in the `/samples` directory here.

From there, you can run:

```bash
./akai.sh samples OUTPUT_FILE
```

## This Sounds Annoying

It's far worse without the script, trust me 🫠
