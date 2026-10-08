# Aivory Desktop

A small Electron shell that loads an existing Aivory website. Chat, streaming,
administration, settings, file previews, and all other features use the same
frontend and server as the web app. Updating the website updates the desktop
interface on its next load; no duplicate frontend build is needed.

## Requirements

- Node.js 22.12 or newer and npm.
- An Aivory deployment serving both the website and its `/api` routes.
- Build each platform on its native OS. The GitHub Actions workflow provides
  Windows, macOS (Apple Silicon and Intel), and Linux builds.

## Configure the Server

`AIVORY_DESKTOP_BASE_URL` is optional during development and packaging. Use the
website origin, such as `https://chat.example.com`, without `/api`, other paths,
credentials, queries, or fragments. `http://localhost:5173` works for local
development. Use HTTPS for a production deployment.

Either export the variable or create `desktop/.env` from
[`desktop/.env.example`](.env.example). Precedence is the process environment,
then `desktop/.env.local`, then `desktop/.env`. The root/server `.env` is not
loaded. The package contains only the validated optional default server URL.

On first launch, the app saves its packaged default to Electron
`userData/server.json`. Without a default or saved address, a bundled setup page
asks for a server. Saved addresses take precedence on subsequent launches,
including updates with an empty or different default. Open Server Settings from
the icon or View menu to change it. Opening settings or saving the same address
preserves drafts. Changing servers reloads the window with a separate cookie and
storage partition. End users do not need environment variables.

## Run and Build

From the repository root:

```bash
npm run desktop:install

# Run against your existing local Vite server
AIVORY_DESKTOP_BASE_URL=http://127.0.0.1:5173 npm run desktop:dev

# Build installers for the current OS
AIVORY_DESKTOP_BASE_URL=https://chat.example.com npm run desktop:build

# Generic package with first-launch server setup
AIVORY_DESKTOP_BASE_URL= npm run desktop:build

# Build an unpacked app without creating an installer
AIVORY_DESKTOP_BASE_URL=https://chat.example.com npm run desktop:pack
```

On PowerShell:

```powershell
npm run desktop:install
$env:AIVORY_DESKTOP_BASE_URL = "https://chat.example.com"
npm run desktop:dev
npm run desktop:build
```

Additional electron-builder flags are forwarded:

```bash
npm run desktop:build -- --win --x64
npm run desktop:build -- --mac --arm64
npm run desktop:build -- --mac --x64
npm run desktop:build -- --linux --x64
```

The app version comes from the root `package.json`. Artifacts are written to
`desktop/release/`: Windows `.exe`, macOS `.dmg` and `.zip`, Linux `.AppImage`
and `.deb`. The current Aivory logo is rendered to a 1024px icon and converted
into native macOS/Windows/Linux formats; the development Dock icon also uses it.
Desktop dependencies are installed separately; ordinary web builds do not need
Electron or electron-builder.

## GitHub Actions

Open **Actions → Build desktop app → Run workflow** and optionally enter your website URL.
Leave it empty for a generic installer.
Download installers from the workflow's artifacts. Optionally enter an existing
published `release_tag`: after every platform builds successfully, installers are
attached to that Release. Its tag must match the checked-out package version.
Leaving the tag empty only produces artifacts. No Release or app store entry is created.

Publishing a Release (including a prerelease) automatically builds generic
installers from its tag on Windows, Linux, and both macOS architectures.
After all four builds succeed, the workflow uploads the installers and
`SHA256SUMS.txt` to that Release. Automatic builds leave the default server
empty so each deployment's users can configure their own website.

Configure the web **Download App** entry in **Admin → System → Desktop client**
(`/admin/settings/desktop`). Enter a download page or direct installer URL,
turn on visibility, and save. Both the new-conversation welcome screen and the
avatar menu open this URL in a new tab. Native desktop clients hide both entries.
The entry is independent of update publication and is hidden by default.

Manage deployment-specific desktop updates in **Admin → System → Desktop client**
(`/admin/settings/desktop`). The admin console reuses the cached GitHub release
check and displays a notice when a newer version is available. Build installers
for your server, host them on your own GitHub Release, object storage, or download
server, and save their version and per-platform URLs. Enable publication only
after the packages are ready. Selecting another upstream version clears the old
URLs and disables publication; unconfigured platforms receive no update prompt.
Alternatively select **Official packages** to use completed installers attached
to the matching official Release. First-time users configure a server; users
updating this client retain their saved address. Official URLs are snapshotted
when the administrator publishes, so ordinary client checks never call GitHub.

The app checks its configured website's `/api/public/desktop-update` 15 seconds
after startup and every four hours. Manual checks are available in the icon menu,
Help menu, and Settings → About. Only operator-published newer versions with a
matching OS/architecture URL prompt users. Stable apps ignore prereleases;
beta apps also accept newer betas. The simple confirmation opens the configured
installer URL in the system browser. Quit and install it to update. Desktop
checks use only the packages published by the administrator.
Deploy the matching backend; older servers without this endpoint do not provide
desktop updates. Existing clients using the old GitHub update logic must first
install and launch a client with local server persistence before a generic
update can automatically retain their address.

The default CI builds are unsigned. For signed distribution, use
electron-builder's signing variables (`CSC_LINK` / `CSC_KEY_PASSWORD` for
macOS; `WIN_CSC_LINK` / `WIN_CSC_KEY_PASSWORD` for Windows). macOS notarization
additionally requires your Apple credentials and an appropriate Developer ID
certificate. Pass credentials through your build environment or CI secrets;
do not put them in tracked files. Electron-builder handles signing during the
normal build command.

## Runtime Behavior

- Closing the main window minimizes it, preserving content and its taskbar/Dock
  icon. The macOS menu bar and Windows/Linux system tray show the Aivory icon;
  the macOS template icon adapts to light/dark system appearance.
  Quit from these menus or the macOS Dock menu. Packaged
  Windows builds also provide a taskbar Quit task. Normal application Quit and
  system shutdown still exit the process.
  If native minimization is unavailable on macOS, the window hides while the
  Dock icon stays available to restore it.
- Cookies and local storage persist across restarts and are isolated by server.
- Desktop requests append `AivoryDesktop/{version}` to the User-Agent. Feedback,
  login history, active sessions, and audit records identify these as the desktop
  app. Regular browser records retain their original browser information.
- Relative `/api` calls, SSE, WebSockets, uploads, and previews remain same-origin.
- Full-page payment redirects stay in the app and retain the session.
- Same-origin popup pages and blob previews open in isolated desktop windows.
  External links opening a new window use the system browser.
- Downloads use the system save dialog; voice features may use the microphone.
- The remote page has no Node.js or unrestricted Electron API access. Its
  sandboxed bridge exposes only app info, browser login, login cancellation, and
  update checking. The main process accepts only the configured site's main
  window and main frame. Sandboxing, context
  isolation, browser security, and certificate verification stay enabled.
- Native menu and connection-error text follow the OS language, with English,
  Simplified/Traditional Chinese, Japanese, and French strings. Website language
  and theme continue to use the existing settings.
- Network loss shows a bundled, borderless offline view even if the website
  cannot load. It follows the website's language, light/dark mode, and accent
  when the page was already loaded; startup defaults to the OS language/theme.
  The website renderer stays alive behind the offline view, preserving drafts.
  Reconnecting restores that page without refreshing it. Initial connection
  failures offer retry and browser access and retry automatically every 10
  seconds while the browser reports an available network. Only offline recovery
  sends these lightweight probes; normal requests remain untouched.

Keep the server's `ALLOWED_ORIGINS` configured with the website origin as for a
normal browser deployment. No `file://` origin or special desktop CORS exception
is needed.

## System Browser Login

Choose **Sign in using your browser**. The destination always uses the saved
base URL: `https://chat.example.com` opens
`https://chat.example.com/desktop/authorize?request_id=…`. Sign in using the
website's existing password, OAuth, passkey, and two-factor flows, then confirm
the account. The desktop restores its window and creates its own persistent
session. An existing browser session can authorize immediately. Requests expire
after five minutes and can be cancelled from either client.
The desktop login page offers only browser authorization and password login
when enabled by the administrator. OAuth and passkey login remain available in
the system browser; the desktop page does not display these buttons or trigger
the website's automatic OAuth entry. Its login form omits the welcome title
and subtitle; the two-factor verification instructions remain visible.
Ordinary web login is unchanged.

Deploy the matching frontend and backend with `/api/auth/desktop/*` support.
S256 proof and an expiring single-use grant bind the desktop session to the
browser's authorized account. Long-lived credentials and the proof verifier
never appear in browser URLs. Polling happens every two seconds only while
authorization is pending; normal chat and streaming requests are unchanged.
Existing cache infrastructure is reused with no database schema changes;
multi-instance deployments use the existing shared Redis cache.

## Verification

```bash
npm run desktop:test
# Requires the existing root dependencies (puppeteer-core) and a graphical OS
npm run desktop:test:smoke
npm run desktop:test:server
npm run desktop:test:updates
npm run desktop:test:records
npm run desktop:test:auth
npm run desktop:test:download
```

The smoke test starts the real Electron shell against local fixtures, checks
cookies, streaming, popups, redirects, offline recovery, preserved drafts, and session
persistence, close/minimize/restore/quit, update prompts, and browser authorization,
then closes its processes. Permission policy tests do not request
OS microphone access. Its temporary data stays outside the
repository.
