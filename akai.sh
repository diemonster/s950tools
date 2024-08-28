#!/bin/bash

HXCFE_CMD="./lib/hxcfe_cmdline/App/hxcfe"  # Location of the hxcfe command-line tool
AKAIUTIL_CMD="./lib/akaiutil"              # Location of the akaiutil binary
INPUT_HFE_FILE="./lib/image/DSK.HFE"       # Base Akai s950 HFE file to use for conversion

main() {
    validate_input "$@"
    convert_hfe_to_img
    convert_wav_files
    check_file_size
    change_file_name
    add_files_to_img
    convert_img_to_hfe
}

show_help() {
    echo "Usage: $(basename "$0") [-v] <files_path> [output_hfe_file]"
    echo
    echo "Positional Arguments:"
    echo "  <files_path>      : Path to the file or directory containing files to add (required)."
    echo "  [output_hfe_file] : The output HFE file path (optional, defaults to current working directory)."
    echo
    echo "Options:"
    echo "  -v                : Enable verbose mode."
    echo
    echo "Example:"
    echo "  $(basename "$0") -v /path/to/files_or_directory /optional/path/to/output_file.hfe"
    echo
    echo "Notes:"
    echo "  - Ensure that the total size of files in <files_path> does not exceed 2MB."
    echo "  - The script requires the hxcfe and akaiutil binaries to be correctly configured."
    echo
}

validate_input() {
    if [ $# -lt 1 ]; then
        echo "Error: Not enough arguments provided."
        show_help
        exit 1
    fi

    FILES_PATH="$1"

    if [ $# -eq 2 ]; then
        OUTPUT_HFE_FILE="$2"
    else
        BASE_NAME=$(basename "$(find "$FILES_PATH" -type f -name '*.p9' | head -n 1)" .p9)
        if [ -z "$BASE_NAME" ]; then
            if [ -t 0 ]; then
                read -p "No .p9 file found. Please enter the output HFE filename (without extension): " BASE_NAME
                [ -z "$BASE_NAME" ] && BASE_NAME="OUTPUT"
            else
                BASE_NAME="OUTPUT"
            fi
        fi
        OUTPUT_HFE_FILE="$(pwd)/${BASE_NAME}.HFE"
    fi

    # Ensure the output filename ends in .HFE, and avoid double extensions
    if [[ "${OUTPUT_HFE_FILE}" != *.HFE ]]; then
        OUTPUT_HFE_FILE="${OUTPUT_HFE_FILE}.HFE"
    fi

    OUTPUT_HFE_FILE=$(echo "$OUTPUT_HFE_FILE" | tr '[:lower:]' '[:upper:]')

    if [ ! -f "$INPUT_HFE_FILE" ]; then
        echo "Error: Input HFE file '$INPUT_HFE_FILE' does not exist."
        exit 1
    fi

    if [ ! -d "$FILES_PATH" ] && [ ! -f "$FILES_PATH" ]; then
        echo "Error: '$FILES_PATH' is neither a valid file nor directory."
        exit 1
    fi
}

convert_hfe_to_img() {
    IMG_FILE="$(pwd)/working_file.img"
    $HXCFE_CMD -finput:"$INPUT_HFE_FILE" -conv:RAW_LOADER -foutput:"$IMG_FILE"

    if [ ! -f "$IMG_FILE" ]; then
        echo "Error: Failed to convert HFE to IMG." >&3
        exit 1
    fi

    echo "Step 1: Conversion from HFE to IMG completed." >&3
}

convert_wav_files() {
    for wav_file in "$FILES_PATH"/*.wav; do
        if [ -f "$wav_file" ]; then
            echo "Converting $wav_file to S900 format..." >&3
            $AKAIUTIL_CMD "$IMG_FILE" <<EOF
wav2sample9 "$wav_file"
exit
EOF
            if [ $? -ne 0 ]; then
                echo "Error: Failed to convert WAV file '$wav_file'." >&3
                exit 1
            fi
            echo "Conversion of $wav_file completed." >&3
        fi
    done
}

check_file_size() {
    TOTAL_SIZE=0

    if [ -d "$FILES_PATH" ]; then
        for file in "$FILES_PATH"/*; do
            FILE_SIZE=$(stat -f%z "$file")
            TOTAL_SIZE=$((TOTAL_SIZE + FILE_SIZE))
        done
    else
        FILE_SIZE=$(stat -f%z "$FILES_PATH")
        TOTAL_SIZE=$((TOTAL_SIZE + FILE_SIZE))
    fi

    if [ $TOTAL_SIZE -gt 2097152 ]; then
        echo "Error: Total size of files exceeds 2MB." >&3
        exit 1
    fi
}

change_file_name() {
    for file in "$FILES_PATH"/*; do
        if [[ "$file" == *.p9.p9 ]]; then
            new_name=$(echo "$file" | sed 's/.p9.p9$/.p9/')
            mv "$file" "$new_name"
            echo "Renamed $file to $new_name" >&3
        fi
    done
}

add_files_to_img() {
    $AKAIUTIL_CMD "$IMG_FILE" <<EOF
lcd ${FILES_PATH}
put *
exit
EOF

    if [ $? -ne 0 ]; then
        echo "Error: Failed to add files to IMG file using akaiutil." >&3
        exit 1
    fi

    echo "Step 2: Files added to IMG file using akaiutil." >&3
}

convert_img_to_hfe() {
    $HXCFE_CMD -finput:"$IMG_FILE" -uselayout:"AKAIS950_HD_1600KB" -conv:HXC_HFE -foutput:"$OUTPUT_HFE_FILE"

    if [ ! -f "$OUTPUT_HFE_FILE" ]; then
        echo "Error: Failed to convert IMG back to HFE." >&3
        exit 1
    fi

    echo "Step 3: Conversion from IMG back to HFE completed." >&3

    rm -f "$IMG_FILE"

    echo "Process completed successfully. Output file: $OUTPUT_HFE_FILE" >&3
}

# some simple CLI parsing
if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    show_help
    exit 0
elif [[ "$1" == "-v" ]]; then
    VERBOSE=true
    shift
    main "$@"
else
    VERBOSE=false
    exec 3>&1 1>akai.log 2>&1
    main "$@"
fi
