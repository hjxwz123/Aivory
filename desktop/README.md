# Aivory Desktop

An Electron client that runs the packaged Aivory frontend locally. The installer
includes the HTML, JavaScript, styles, fonts, icons and preview dependencies.
Only API requests go to the configured Aivory server; the app never loads that
server's homepage or frontend assets. The same frontend source powers the web
and desktop apps, with explicit desktop behavior for login, transport and updates.

## Requirements

- Node.js 22.12 or newer and npm.
- An Aivory backend exposing its `/api` routes. Ordinary desktop use does not
  require a website; browser authorization and public conversation links also
  require the deployment's web frontend.
- Build each platform on its native OS. The GitHub Actions workflow provides
  Windows, macOS (Apple Silicon and Intel), and Linux builds.

## Configure the Server

`AIVORY_DESKTOP_BASE_URL` is optional during development and packaging. Use the
server origin, such as `https://chat.example.com`, without `/api`, other paths,
credentials, queries, or fragments. Use the backend port (for example
`http://localhost:8080`) for local development, or a proxy exposing `/api`.
Use HTTPS for a production deployment.

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
npm ci

# Build the local frontend and run against your existing API server
AIVORY_DESKTOP_BASE_URL=http://127.0.0.1:8080 npm run desktop:dev

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
Desktop development and packaging first build the frontend into `dist-desktop`
and copy it into the application. A running Vite server is not required. Source
changes need a new desktop development build; web development still uses Vite's
normal hot reload.

## GitHub Actions

Open **Actions → Build desktop app → Run workflow** and optionally enter your server URL.
Leave it empty for a generic installer.
Download installers from the workflow's artifacts. Optionally enter an existing
published `release_tag`: after every platform builds successfully, installers are
attached to that Release. Its tag must match the checked-out package version.
Leaving the tag empty only produces artifacts. No Release or app store entry is created.

Publishing a Release (including a prerelease) automatically builds generic
installers from its tag on Windows, Linux, and both macOS architectures.
After all four builds succeed, the workflow uploads the installers and
`SHA256SUMS.txt` to that Release. Automatic builds leave the default server
empty so each deployment's users can configure their own server. Every platform
installs the frontend dependencies and embeds a fresh frontend build.

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

The app checks its configured server's `/api/public/desktop-update` 15 seconds
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

Without an Apple Developer certificate, macOS builds use ad-hoc signing to
replace Electron's original signatures after packaging. CI strictly verifies
the app, the app extracted from the ZIP, and the app mounted from the DMG before
upload. Invalid or missing bundles fail the build. This repairs malformed bundle
signatures, but ad-hoc signatures do not establish a trusted developer identity
or provide Apple notarization. Gatekeeper may still block a browser download.

For your own trusted download, first move Aivory to Applications, then use
macOS **System Settings → Privacy & Security → Open Anyway** if available.
If macOS labels an unnotarized download as damaged and offers no override,
remove the quarantine flag from this app only:

```bash
xattr -dr com.apple.quarantine /Applications/Aivory.app
```

Do this only for a package from a source you trust; do not disable Gatekeeper
globally. Packages published before this fix need to be replaced by a rebuilt
package with valid signatures.

For trusted distribution, configure GitHub Secrets `MAC_CSC_LINK` (a Developer
ID Application certificate), `MAC_CSC_KEY_PASSWORD`, `APPLE_ID`,
`APPLE_APP_SPECIFIC_PASSWORD`, and `APPLE_TEAM_ID`. The existing workflow passes
them to electron-builder and enables notarization when all Apple credentials
are present. Local builds use `CSC_LINK` / `CSC_KEY_PASSWORD` and the same Apple
variables. Windows signing uses `WIN_CSC_LINK` / `WIN_CSC_KEY_PASSWORD`.
Keep credentials out of tracked files.

## Runtime Behavior

- macOS uses native traffic lights in a compact, draggable area that follows the
  page theme. It reserves space above the app, so window controls do not
  cover page navigation, setup, or offline recovery.
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
- Electron resolves `https://app.aivory.invalid/` through a local protocol
  handler. This is a virtual local origin, with no DNS lookup, HTTP listener or
  local server process. Application routes and static assets resolve to bundled
  files; deep links and page reloads retain local routing.
- Relative `/api` calls go through the native session to the configured backend.
  Request methods, signed paths, bodies and authorization headers are preserved.
  Upload and response streams pass through without buffering an entire file or
  waiting for a chat response to finish. Remote HttpOnly cookies stay in the
  server's native cookie jar and are never exposed to the local renderer.
  A scoped cancellation bridge closes native requests when a user stops
  generation, cancels an upload/preview, or switches servers.
- Live voice uses a native WebSocket bridge to the configured `/api/audio/stream`.
  Both HTTP/WS and HTTPS/WSS backends work. Web clients retain their normal
  browser WebSocket transport. Microphone permission belongs to the local app.
- Payment redirects and form POSTs use separate sandboxed checkout windows,
  without the desktop bridge. Returning to the server's subscription page
  closes checkout and navigates the local app to its subscription page.
- Local popup pages and blob previews open in isolated desktop windows.
  External websites use the system browser. Share, invite and HTML-preview
  links refer to the configured server so other users can open them.
- Downloads use the system save dialog; voice features may use the microphone.
- The local page has no Node.js or unrestricted Electron API access. Its
  sandboxed bridge exposes app info, browser login, login cancellation, update
  checking, payment checkout and the fixed voice endpoint. The main process
  accepts these calls only from the local application's main window and main
  frame. Remote webpages cannot access the bridge. Sandboxing, context
  isolation, browser security, and certificate verification stay enabled.
- Native menu and connection-error text follow the OS language, with English,
  Simplified/Traditional Chinese, Japanese, and French strings. Website language
  and theme continue to use the existing settings.
- Network loss shows a bundled, borderless offline view even if the API server
  cannot load. It follows the app's language, light/dark mode, and accent
  when the page was already loaded; startup defaults to the OS language/theme.
  The local renderer stays alive behind the offline view, preserving drafts.
  Reconnecting restores that page without refreshing it. Initial connection
  failures offer retry and browser access and retry automatically every 10
  seconds while the browser reports an available network. Only offline recovery
  sends these lightweight probes; normal requests remain untouched.
- Desktop UI updates require a new installer. Deploying a new web frontend does
  not change the installed desktop frontend. Web service-worker registration and
  web version-reload checks are disabled inside the desktop app; administrator
  published desktop installer updates continue to work. Web behavior is unchanged.

Keep the server's `ALLOWED_ORIGINS` configured with its deployment origin as for a
normal browser deployment. Native API requests present that server origin to
the existing CSRF checks. No `file://` origin, permissive CORS rule or disabled
renderer web security is needed.

## System Browser Login

Choose **Sign in using your browser**. The destination always uses the saved
base URL: `https://chat.example.com` opens
`https://chat.example.com/desktop/authorize?request_id=…`. Sign in using the
website's existing password, OAuth, passkey, and two-factor flows, then confirm
the account. The desktop restores its window and creates its own persistent
session in place, preserving the requested app route without reloading the local
frontend. Consent shows the app version, OS and request IP/location when the
server provides them. An existing browser session can authorize immediately. Requests expire
after five minutes and can be cancelled from either client.
The desktop login page offers only browser authorization and password login
when enabled by the administrator. OAuth and passkey login remain available in
the system browser; the desktop page does not display these buttons or trigger
the website's automatic OAuth entry. Its login form omits the welcome title
and subtitle; the two-factor verification instructions remain visible.
Ordinary web login is unchanged.

Passkey registration must run on the server's real domain, so Settings → Account
opens the configured server's account page in the system browser to add a
passkey. The desktop can still list and remove existing passkeys, and refreshes
the list when it regains focus. The local app never runs a WebAuthn ceremony.
It retains sessions through its server-specific cookie jar; it does not offer
the web browser's Remember password checkbox or store plaintext passwords.
Starting password login first cancels and settles any pending browser exchange.

The app's local device ID is a per-server UI/request identifier, not a hardware
fingerprint or session ID. Moving from a remotely loaded website to the bundled
frontend creates new local storage once; existing native cookies are still kept
in the same per-server partition. Login history and session revocation use the
server's session family, independently of that local device ID.

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
# Verify a completed Mac build and launch the signed package itself
node desktop/verify-mac.mjs desktop/release
node tests/browser/run-desktop-package.mjs /path/to/Aivory.app/Contents/MacOS/Aivory
```

The smoke test starts the real Electron client with a bundled fixture frontend
and API fixtures, checks that no server frontend is loaded, and verifies signed
requests, uploads, compressed responses, cookies, streaming/cancellation,
native voice frames, checkout, popups, offline recovery, preserved drafts, and session
persistence, close/minimize/restore/quit, update prompts, and browser authorization,
then closes its processes. Permission policy tests do not request
OS microphone access. Its temporary data stays outside the
repository.
