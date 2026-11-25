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
1. On your own computer, forward the login port over SSH and leave it running:
     ssh -N -L 127.0.0.1:38183:127.0.0.1:38183 nvidia7
2. Open this link in a browser on your own computer:
     http://127.0.0.1:38183/#…
```

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

Click the picture of the remote page and type as usual. Scrolling, Enter, Tab,
arrow keys, IME input, and paste work. The page shows which site the remote
browser is on, without the path or query. Check it before you type a password
or code.

- The link works in one browser tab. Reloading that tab is fine.
- The link stops working when sign-in completes, when you cancel, or when the
  login times out. The timeout is `browser.login_timeout` (five minutes by
  default) and never more than ten minutes.
- While the remote page is loading or redirecting, the view shows the last
  picture and updates within a second or two.
- If an input may not have arrived, the view says so and waits for you. It
  never sends the same input twice by itself. Look at the screen, then
  continue.
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
form submission, page navigation, and a login that ends early. On 2026-11-25 the
forwarded view was run on a Linux server without a display, against the real
Outlook Web sign-in page, up to the point of entering credentials. A complete
sign-in from a Mac, and the same flow inside Orca, have not been recorded yet.
Never put real credentials into fixtures, logs, issue reports, screenshots, or
agent prompts.
