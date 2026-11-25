# Signing in on a host without a display

An Outlook Web account signs in through a browser that Corresync runs next to
the account. On a server you reach over SSH there is no screen to show that
browser on. Corresync then runs the browser headless on the server and shows
its sign-in page in a browser on your own computer. The session stays on the
server; nothing is signed in on your own computer.

This covers Outlook Web. Teams Web uses a separate browser and is not supported
here yet.

## On the server

Run the normal login command:

```console
corr auth login --account work
```

On Linux, when neither `DISPLAY` nor `WAYLAND_DISPLAY` is set, this starts the
forwarded login view by itself. `--web` asks for the same view explicitly, for
example on a host that has a display you cannot see. The command prints two
things to do on your own computer:

```text
1. On your own computer, start this SSH tunnel and leave it running:

   ssh -N -L 127.0.0.1:38183:127.0.0.1:38183 nvidia7

   If you reach this server by another name, use that instead of nvidia7.

2. Open this link in a browser there. It is private; do not share it.

http://127.0.0.1:38183/#…
```

The link is longer than 80 characters and is printed on a line of its own, so
a narrow terminal wraps only the link.

The host name in the `ssh` line is the server's own name. Replace it with
whatever you normally type to reach the server, including a `user@` prefix or an
alias from `~/.ssh/config`. The port must be the same on both sides. If it is
taken on your computer, start again with `--web-port PORT` and a port that is
free on both.

Leave the `corr` command and the `ssh` tunnel running until the page says
sign-in is complete, then stop the tunnel with Ctrl-C.

### One command from your own computer

If you would rather not keep two terminals open, start the remote command and
the tunnel together. Pick a port that is free on both computers:

```console
ssh -t -L 127.0.0.1:47800:127.0.0.1:47800 nvidia7 \
  '~/.local/bin/corr auth login --account work --web --web-port 47800'
```

Use the path where `corr` is installed on the server. A command passed to `ssh`
does not read your login profile, so a bare `corr` is often not found.

## From your own computer, with Corresync installed on both

If the same Corresync version is installed on your own computer, it can do the
forwarding for you:

```console
corr auth login --host nvidia7 --account work
```

`--host` takes the name you use with `ssh`. Put ports and jump hosts in
`~/.ssh/config`. The command starts a helper on the server over your normal SSH
connection, opens the login view in your local browser, and prints the link in
case the browser does not open. It sends screen images and your input through
that SSH connection and opens no network port on the server. The helper looks
for `corr` on the server's `PATH` and then in `~/.local/bin`, `/usr/local/bin`,
and the Homebrew directories.

## Using the login view

The top of the page says what is happening: starting, loading, ready, check
the screen, reconnecting, or how the login ended (signed in, cancelled,
expired, could not start). Next to the title it shows how long the link stays
valid. Below that it names the site the remote browser is on, for example
"Signing in at login.microsoftonline.com", without the path or query. Check it
before you type a password or code.

Click the picture of the remote page and type as usual. The line under the
picture says when your typing goes to the remote page. Scrolling, Enter, Tab,
arrow keys, and IME input work. Moving the pointer over the picture moves it on
the remote page too, so menus that open on hover work.

To paste, press **Paste** or Ctrl+V (⌘V on a Mac). Either sends the text on
your clipboard, up to 4 KB, as if you had typed it. The page reads the
clipboard only when you do one of these. If the browser does not allow the
Paste button to read the clipboard, the page says so; click the picture and
press Ctrl+V or ⌘V instead.

- The link works in one browser tab. Reloading that tab is fine.
- The link stops working when sign-in completes, when you cancel, or when the
  login times out. The timeout is `browser.login_timeout` (five minutes by
  default) and never more than ten minutes. With `--host` the page counts down
  the sooner of this computer's and the server's limits.
- The picture refreshes several times a second for a few seconds after you do
  something and once a second otherwise. A picture that has not changed is not
  sent again.
- While the remote page is loading or redirecting, the view keeps the last
  picture and shows "Loading…" on it.
- If an input may not have arrived, the view says "Check the screen" and stops
  taking input. It never sends the same input twice by itself. Press **Refresh
  view**, look at the screen, then continue. A pointer movement that does not
  arrive is simply dropped.
- Ctrl-C in the terminal, or **Cancel login** on the page, closes the remote
  browser. Closing the tab alone does not.

Starting `corr auth login` again for the same account replaces a login that was
left unfinished, for example after the SSH connection dropped. The earlier
command stops with a message; its browser is closed, not reused.

## What it cannot do

Touch ID and other passkeys stored on your own computer, USB security keys, and
client certificates are not forwarded. If your organization requires one of
them, sign in on a machine where the provider's own sign-in can use it. Nothing
here bypasses MFA, Conditional Access, or a permission check.

Do not expose the login view through a public tunnel, bind it to another
network interface, or share the link. The page keeps neither the pictures nor
what you type.

`corr auth login --terminal` is still available as a text-only alternative.

## If it does not work

- **The page says the login could not start, or has ended.** The terminal
  running `corr` prints the reason. Run the command again.
- **The browser cannot reach `127.0.0.1`.** The tunnel is not running, or it
  forwards a different port. The `ssh -L` line must use the port printed by
  `corr`.
- **`--host` reports that the SSH connection closed.** Check that `ssh HOST`
  works without a prompt and that the same Corresync version is installed on
  the server. Any message from the server is printed just above.
- **The remote page stays blank.** The server may not be able to reach the
  provider. Try `corr doctor` on the server.

## How much of this has been checked

Automated tests drive the login view in a real Chromium, including input,
paste, pointer movement, form submission, page navigation, the remaining
time, and a login that ends early. The paste, pointer movement, and refresh
changes of the next release after 0.9.5 have not been run against a real
provider yet. On 2026-11-25 the
forwarded view was run on a Linux server without a display, against the real
Outlook Web sign-in page, up to the point of entering credentials. A complete
sign-in from a Mac, and the same flow inside Orca, have not been recorded yet.
Never put real credentials into fixtures, logs, issue reports, screenshots, or
agent prompts.
