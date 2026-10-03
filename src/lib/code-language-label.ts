const LANG_LABELS: Record<string, string> = {
  js: 'JavaScript', javascript: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
  ts: 'TypeScript', typescript: 'TypeScript', jsx: 'JSX', tsx: 'TSX',
  py: 'Python', python: 'Python', python3: 'Python', rb: 'Ruby', go: 'Go', golang: 'Go',
  rs: 'Rust', rust: 'Rust', java: 'Java', kt: 'Kotlin', kotlin: 'Kotlin',
  cs: 'C#', csharp: 'C#', cpp: 'C++', 'c++': 'C++', c: 'C', objc: 'Objective-C',
  php: 'PHP', sh: 'Shell', bash: 'Bash', zsh: 'Zsh', shell: 'Shell', ps1: 'PowerShell',
  html: 'HTML', htm: 'HTML', xml: 'XML', css: 'CSS', scss: 'SCSS', sass: 'Sass', less: 'Less',
  json: 'JSON', jsonc: 'JSON', yaml: 'YAML', yml: 'YAML', toml: 'TOML',
  sql: 'SQL', md: 'Markdown', markdown: 'Markdown', diff: 'Diff', graphql: 'GraphQL',
  swift: 'Swift', dart: 'Dart', scala: 'Scala', r: 'R', lua: 'Lua', dockerfile: 'Dockerfile',
}

export function codeLanguageLabel(lang?: string): string {
  if (!lang) return 'Plain'
  const key = lang.toLowerCase()
  return LANG_LABELS[key] ?? key.charAt(0).toUpperCase() + key.slice(1)
}
