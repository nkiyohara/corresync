# fish completion for Corresync
function __corresync_complete
    set -lx COMP_LINE (commandline -cp)
    test -z (commandline -ct); and set COMP_LINE "$COMP_LINE "
    command corresync
end
complete -f -c corresync -a "(__corresync_complete)"
