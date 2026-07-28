# fish completion for Corresync
function __corr_complete
    set -lx COMP_LINE (commandline -cp)
    test -z (commandline -ct); and set COMP_LINE "$COMP_LINE "
    command corr
end
complete -f -c corr -a "(__corr_complete)"
