export interface AdminNavItem {
  to: string
  labelKey: string
  defaultLabel: string
  /** Drill-down routes that should keep this item selected. */
  also?: readonly string[]
}

export type AdminNavGroupKey =
  | 'ai'
  | 'capabilities'
  | 'access'
  | 'billing'
  | 'operations'
  | 'platform'

export interface AdminNavGroup {
  key: AdminNavGroupKey
  /** Stable destination for the group entry, independent of tab ordering. */
  to: string
  labelKey: string
  defaultLabel: string
  items: readonly AdminNavItem[]
}

export const ADMIN_OVERVIEW: AdminNavItem = {
  to: '/admin/overview',
  labelKey: 'admin:menu.overview',
  defaultLabel: 'Overview',
}

export const ADMIN_NAV_GROUPS: readonly AdminNavGroup[] = [
  {
    key: 'ai',
    to: '/admin/channels',
    labelKey: 'admin:menu.aiModels',
    defaultLabel: 'AI & models',
    items: [
      { to: '/admin/channels', labelKey: 'admin:channels.title', defaultLabel: 'Channels' },
      {
        to: '/admin/models',
        labelKey: 'admin:models.title',
        defaultLabel: 'Models',
        also: ['/admin/model-tags'],
      },
      {
        to: '/admin/settings/model-policy',
        labelKey: 'admin:menu.modelPolicy',
        defaultLabel: 'Model policy',
      },
      {
        to: '/admin/settings/context-memory',
        labelKey: 'admin:menu.contextMemory',
        defaultLabel: 'Context & memory',
      },
      { to: '/admin/moderation', labelKey: 'admin:moderation.title', defaultLabel: 'Moderation' },
    ],
  },
  {
    key: 'capabilities',
    to: '/admin/skills',
    labelKey: 'admin:menu.capabilities',
    defaultLabel: 'Capabilities & integrations',
    items: [
      { to: '/admin/skills', labelKey: 'admin:skills.title', defaultLabel: 'Skills' },
      { to: '/admin/prompts', labelKey: 'admin:prompts.title', defaultLabel: 'Prompt library' },
      { to: '/admin/tools', labelKey: 'admin:tools.title', defaultLabel: 'Tools' },
      { to: '/admin/mcp', labelKey: 'admin:mcp.title', defaultLabel: 'MCP services' },
      { to: '/admin/documents', labelKey: 'admin:documents.title', defaultLabel: 'Documents & knowledge' },
      { to: '/admin/image-styles', labelKey: 'admin:imageStyles.title', defaultLabel: 'Image generation' },
      { to: '/admin/audio', labelKey: 'admin:audio.title', defaultLabel: 'Speech' },
    ],
  },
  {
    key: 'access',
    to: '/admin/users',
    labelKey: 'admin:menu.usersAccess',
    defaultLabel: 'Users & access',
    items: [
      { to: '/admin/users', labelKey: 'admin:users.title', defaultLabel: 'Users' },
      {
        to: '/admin/settings/registration',
        labelKey: 'admin:menu.registrationPolicy',
        defaultLabel: 'Registration policy',
      },
      { to: '/admin/oauth', labelKey: 'admin:oauth.title', defaultLabel: 'Login providers' },
      { to: '/admin/workspaces', labelKey: 'admin:workspaces.title', defaultLabel: 'Workspaces' },
    ],
  },
  {
    key: 'billing',
    to: '/admin/user-groups',
    labelKey: 'admin:menu.billing',
    defaultLabel: 'Billing & entitlements',
    items: [
      { to: '/admin/user-groups', labelKey: 'admin:groups.title', defaultLabel: 'Plans' },
      { to: '/admin/credits', labelKey: 'admin:menu.creditsQuotas', defaultLabel: 'Credits & quotas' },
      { to: '/admin/redeem-codes', labelKey: 'admin:redeemCodes.title', defaultLabel: 'Redeem codes' },
      {
        to: '/admin/payment-channels',
        labelKey: 'admin:paymentChannels.title',
        defaultLabel: 'Payment channels',
      },
      {
        to: '/admin/payment-methods',
        labelKey: 'admin:paymentMethods.title',
        defaultLabel: 'Payment methods',
      },
      { to: '/admin/payment-orders', labelKey: 'admin:paymentOrders.title', defaultLabel: 'Payment orders' },
    ],
  },
  {
    key: 'operations',
    to: '/admin/analytics',
    labelKey: 'admin:menu.operations',
    defaultLabel: 'Data & operations',
    items: [
      { to: '/admin/analytics', labelKey: 'admin:analytics.title', defaultLabel: 'Analytics' },
      { to: '/admin/usage', labelKey: 'admin:usage.title', defaultLabel: 'Usage records' },
      { to: '/admin/files', labelKey: 'admin:files.title', defaultLabel: 'Files' },
      { to: '/admin/feedback', labelKey: 'admin:menu.userFeedback', defaultLabel: 'User feedback' },
    ],
  },
  {
    key: 'platform',
    to: '/admin/announcement',
    labelKey: 'admin:menu.platform',
    defaultLabel: 'System',
    items: [
      { to: '/admin/announcement', labelKey: 'admin:announcement.title', defaultLabel: 'Announcement' },
      {
        to: '/admin/settings/email',
        labelKey: 'admin:menu.emailService',
        defaultLabel: 'Email service',
      },
      { to: '/admin/storage', labelKey: 'admin:menu.storageUploads', defaultLabel: 'Storage & uploads' },
      {
        to: '/admin/settings/legal',
        labelKey: 'admin:menu.legalContact',
        defaultLabel: 'Legal & contact',
      },
      {
        to: '/admin/settings/logging',
        labelKey: 'admin:menu.loggingPrivacy',
        defaultLabel: 'Logging & privacy',
      },
      { to: '/admin/backup', labelKey: 'admin:backup.title', defaultLabel: 'Backup & migration' },
    ],
  },
]

export function underAdminPath(path: string, to: string): boolean {
  return path === to || path.startsWith(to.endsWith('/') ? to : `${to}/`)
}

export function adminNavItemActive(path: string, item: AdminNavItem): boolean {
  return underAdminPath(path, item.to)
    || (item.also ?? []).some((prefix) => underAdminPath(path, prefix))
}

export function adminNavGroupActive(path: string, group: AdminNavGroup): boolean {
  return group.items.some((item) => adminNavItemActive(path, item))
}

export function adminNavGroupForPath(path: string): AdminNavGroup | undefined {
  return ADMIN_NAV_GROUPS.find((group) => adminNavGroupActive(path, group))
}
