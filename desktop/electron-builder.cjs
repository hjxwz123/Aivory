const hasMacIdentity = Boolean(process.env.CSC_LINK || process.env.CSC_NAME)
const macEntitlements = hasMacIdentity ? 'entitlements.mac.plist' : 'entitlements.mac.adhoc.plist'

module.exports = {
  appId: 'com.aivory.desktop',
  productName: 'Aivory',
  forceCodeSigning: process.platform === 'darwin' && hasMacIdentity,
  directories: {
    app: 'generated/app',
    output: 'release',
  },
  files: ['package.json', '*.cjs', 'offline.*', 'server.*', 'config.json', 'assets/**/*', 'web/**/*'],
  asar: true,
  npmRebuild: false,
  artifactName: '${productName}-${version}-${os}-${arch}.${ext}',
  icon: 'generated/app/assets/icon.png',
  mac: {
    target: ['dmg', 'zip'],
    // A modified Electron bundle must be re-signed, even without a Developer ID.
    identity: hasMacIdentity ? undefined : '-',
    hardenedRuntime: true,
    entitlements: macEntitlements,
    entitlementsInherit: macEntitlements,
    notarize: Boolean(process.env.APPLE_ID && process.env.APPLE_APP_SPECIFIC_PASSWORD && process.env.APPLE_TEAM_ID),
    category: 'public.app-category.productivity',
    extendInfo: {
      NSMicrophoneUsageDescription: 'Aivory uses the microphone for voice messages and transcription.',
    },
  },
  win: { target: ['nsis'] },
  nsis: {
    oneClick: false,
    allowToChangeInstallationDirectory: true,
    createDesktopShortcut: true,
    createStartMenuShortcut: true,
  },
  linux: {
    target: ['AppImage', 'deb'],
    category: 'Office',
    maintainer: 'Aivory contributors',
  },
}
