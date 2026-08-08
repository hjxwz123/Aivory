type ReleaseStartupDialog = () => void

let locked = false
const waiters: Array<(release: ReleaseStartupDialog) => void> = []

function createRelease(): ReleaseStartupDialog {
  let released = false
  return () => {
    if (released) return
    released = true
    const next = waiters.shift()
    if (next) {
      next(createRelease())
      return
    }
    locked = false
  }
}

// Startup dialogs become eligible together after authentication/onboarding.
// Serialize them so an announcement and an account notice never overlap.
export function acquireStartupDialog(): Promise<ReleaseStartupDialog> {
  return new Promise((resolve) => {
    if (!locked) {
      locked = true
      resolve(createRelease())
      return
    }
    waiters.push(resolve)
  })
}
