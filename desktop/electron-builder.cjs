module.exports = {
  appId: 'com.aivory.desktop',
  productName: 'Aivory',
  directories: {
    app: 'generated/app',
    output: 'release',
  },
  files: ['package.json', '*.cjs', 'offline.*', 'server.*', 'config.json', 'assets/**/*'],
  asar: true,
  npmRebuild: false,
  artifactName: '${productName}-${version}-${os}-${arch}.${ext}',
  icon: 'generated/app/assets/icon.png',
  mac: {
    target: ['dmg', 'zip'],
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
