#!/bin/sh
set -e

# First-run: neither config nor workspace exists.
# If config.json is already mounted but workspace is missing we skip onboard to
# avoid the interactive "Overwrite? (y/n)" prompt hanging in a non-TTY container.
pico_state="${PICOCLAW_HOME:-${HOME}/.picoclaw}"
if [ ! -d "${pico_state}/workspace" ] && [ ! -f "${pico_state}/config.json" ]; then
    picoclaw onboard
    echo ""
    echo "First-run setup complete."
    echo "Edit ${pico_state}/config.json (add your API key, etc.) then restart the container."
    exit 0
fi

exec picoclaw gateway "$@"
