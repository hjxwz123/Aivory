import { lazy, Suspense, useEffect } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { useOpenSettings, type SettingsBackgroundState } from '@/hooks/use-open-settings'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/toaster'
import { CommandMenu } from '@/components/command-menu/command-menu'
import { WelcomeCard } from '@/components/welcome/welcome-card'
import { SetPasswordGate } from '@/components/welcome/set-password-gate'
import { AnnouncementPopup } from '@/components/announcement/announcement-popup'
import { AuthGate } from '@/components/auth/auth-gate'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { useSettings } from '@/store/settings'
import { useConversations } from '@/store/conversations'
import { initAppUpdate, maybeApplyUpdate } from '@/lib/app-update'
import { initRealtime } from '@/lib/realtime'

const Landing = lazy(() => import('@/pages/Landing'))
const ChatLayout = lazy(() => import('@/pages/chat/ChatLayout'))
const ChatHome = lazy(() => import('@/pages/chat/ChatHome'))
const ChatThread = lazy(() => import('@/pages/chat/ChatThread'))
const ProjectsList = lazy(() => import('@/pages/projects/ProjectsList'))
const ProjectDetail = lazy(() => import('@/pages/projects/ProjectDetail'))
const KnowledgeBasesList = lazy(() => import('@/pages/kb/KnowledgeBasesList'))
const KnowledgeBaseDetail = lazy(() => import('@/pages/kb/KnowledgeBaseDetail'))
const AuthLayout = lazy(() => import('@/pages/auth/AuthLayout'))
const Login = lazy(() => import('@/pages/auth/Login'))
const Register = lazy(() => import('@/pages/auth/Register'))
const Setup = lazy(() => import('@/pages/auth/Setup'))
const ForgotPassword = lazy(() => import('@/pages/auth/ForgotPassword'))
const Privacy = lazy(() => import('@/pages/legal/Privacy'))
const Terms = lazy(() => import('@/pages/legal/Terms'))
const SettingsLayout = lazy(() => import('@/pages/settings/SettingsLayout'))
const SettingsAccount = lazy(() => import('@/pages/settings/Account'))
const SettingsAppearance = lazy(() => import('@/pages/settings/Appearance'))
const SettingsModels = lazy(() => import('@/pages/settings/Models'))
const SettingsPersonalization = lazy(() => import('@/pages/settings/Personalization'))
const SettingsPrivacy = lazy(() => import('@/pages/settings/Privacy'))
const SettingsShortcuts = lazy(() => import('@/pages/settings/Shortcuts'))
const SettingsAbout = lazy(() => import('@/pages/settings/About'))
const Subscription = lazy(() => import('@/pages/subscription/Subscription'))
const SharedConversation = lazy(() => import('@/pages/share/SharedConversation'))
const JoinWorkspace = lazy(() => import('@/pages/workspace/JoinWorkspace'))
const AdminLayout = lazy(() => import('@/pages/admin/AdminLayout'))
const AdminChannels = lazy(() => import('@/pages/admin/AdminChannels'))
const AdminModels = lazy(() => import('@/pages/admin/AdminModels'))
const AdminModelEdit = lazy(() => import('@/pages/admin/AdminModelEdit'))
const AdminModelTags = lazy(() => import('@/pages/admin/AdminModelTags'))
const AdminSkills = lazy(() => import('@/pages/admin/AdminSkills'))
const AdminImageStyles = lazy(() => import('@/pages/admin/AdminImageStyles'))
const AdminUsers = lazy(() => import('@/pages/admin/AdminUsers'))
const AdminUserGroups = lazy(() => import('@/pages/admin/AdminUserGroups'))
const AdminWorkspaces = lazy(() => import('@/pages/admin/AdminWorkspaces'))
const AdminUserConversations = lazy(() => import('@/pages/admin/AdminUserConversations'))
const AdminUserConversation = lazy(() => import('@/pages/admin/AdminUserConversation'))
const AdminUserLibrary = lazy(() => import('@/pages/admin/AdminUserLibrary'))
const AdminUsage = lazy(() => import('@/pages/admin/AdminUsage'))
const AdminAnalytics = lazy(() => import('@/pages/admin/AdminAnalytics'))
const AdminSettings = lazy(() => import('@/pages/admin/AdminSettings'))
const AdminBackup = lazy(() => import('@/pages/admin/AdminBackup'))
const AdminModeration = lazy(() => import('@/pages/admin/AdminModeration'))
const AdminAnnouncement = lazy(() => import('@/pages/admin/AdminAnnouncement'))
const AdminDocuments = lazy(() => import('@/pages/admin/AdminDocuments'))
const AdminFiles = lazy(() => import('@/pages/admin/AdminFiles'))
const UserFiles = lazy(() => import('@/pages/files/UserFiles'))
const AdminTools = lazy(() => import('@/pages/admin/AdminTools'))
const AdminAudio = lazy(() => import('@/pages/admin/AdminAudio'))
const AdminOAuth = lazy(() => import('@/pages/admin/AdminOAuth'))
const AdminRedeemCodes = lazy(() => import('@/pages/admin/AdminRedeemCodes'))
const NotFound = lazy(() => import('@/pages/NotFound'))

function GlobalShortcuts() {
  const toggle = useCommandMenu((s) => s.toggle)
  const setOpen = useCommandMenu((s) => s.setOpen)
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const createConversation = useConversations((s) => s.createConversation)
  const navigate = useNavigate()
  const openSettings = useOpenSettings()

  useHotkeys([
    { combo: 'mod+k', whenInputFocused: true, handler: () => toggle() },
    { combo: 'mod+b', whenInputFocused: false, handler: () => toggleSidebar() },
    { combo: 'mod+,', whenInputFocused: false, handler: () => openSettings('account') },
    {
      combo: 'mod+shift+o',
      whenInputFocused: false,
      handler: () => {
        void (async () => {
          const c = await createConversation()
          if (c) navigate(`/chat/${c.id}`)
        })()
      },
    },
    {
      combo: 'mod+/',
      whenInputFocused: false,
      handler: () => openSettings('shortcuts'),
    },
    {
      combo: 'escape',
      whenInputFocused: true,
      handler: () => setOpen(false),
      preventDefault: false,
    },
  ])

  return null
}

function RouteFallback() {
  return (
    <div className="min-h-svh w-full flex items-center justify-center text-[var(--color-fg-subtle)] text-sm">
      <span className="inline-block size-4 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite]" />
    </div>
  )
}

function ScrollToTop() {
  const { pathname } = useLocation()
  useEffect(() => {
    window.scrollTo(0, 0)
  }, [pathname])
  return null
}

export default function App() {
  const location = useLocation()
  // §23: realtime notify stream (multi-device sync) + invisible version
  // upgrades. Both are idempotent singletons; the realtime loop follows the
  // auth store on its own (connects after login, stops on logout).
  useEffect(() => {
    initRealtime()
    initAppUpdate()
  }, [])
  // §settings modal: when opened over a page, `backgroundLocation` holds that
  // page. The main <Routes> then render IT (so it stays live behind the modal),
  // and a second <Routes> renders the settings modal on top — its backdrop-blur
  // blurs the page behind. Absent (deep link / OAuth ?linked / fresh load) the
  // modal renders over the app shell instead.
  const backgroundLocation = (location.state as SettingsBackgroundState | null)?.backgroundLocation
  // A route navigation is a safe, invisible moment to apply a pending upgrade
  // (never fires while a message is streaming — see maybeApplyUpdate). Overlay
  // navigations (the settings modal opening / switching tabs over a live
  // backdrop) don't count: the user perceives them as staying on the page, so
  // a full reload there would be anything but invisible. The upgrade applies
  // on the closing navigation back to the background page instead.
  const isOverlayNavigation = Boolean(backgroundLocation)
  useEffect(() => {
    if (!isOverlayNavigation) maybeApplyUpdate('navigation')
  }, [location.pathname, isOverlayNavigation])

  return (
    <TooltipProvider delayDuration={280} skipDelayDuration={120}>
      <ScrollToTop />
      <AuthGate>
        <GlobalShortcuts />
        <Suspense fallback={<RouteFallback />}>
          <Routes location={backgroundLocation ?? location}>
            <Route path="/welcome" element={<Landing />} />
            <Route path="/share/:token" element={<SharedConversation />} />
            <Route path="/workspace/join/:token" element={<JoinWorkspace />} />
            <Route path="/" element={<ChatLayout />}>
              <Route index element={<ChatHome />} />
              <Route path="chat/:id" element={<ChatThread />} />
            </Route>
            <Route path="/chat" element={<ChatLayout />}>
              <Route index element={<ChatHome />} />
              <Route path=":id" element={<ChatThread />} />
            </Route>
            <Route path="/projects" element={<ChatLayout />}>
              <Route index element={<ProjectsList />} />
              <Route path=":id" element={<ProjectDetail />} />
            </Route>
            <Route path="/files" element={<ChatLayout />}>
              <Route index element={<UserFiles />} />
            </Route>
            <Route path="/kb" element={<ChatLayout />}>
              <Route index element={<KnowledgeBasesList />} />
              <Route path=":id" element={<KnowledgeBaseDetail />} />
            </Route>
            <Route path="/subscription" element={<ChatLayout />}>
              <Route index element={<Subscription />} />
            </Route>
            <Route element={<AuthLayout />}>
              <Route path="/login" element={<Login />} />
              <Route path="/register" element={<Register />} />
              <Route path="/setup" element={<Setup />} />
              <Route path="/forgot-password" element={<ForgotPassword />} />
            </Route>
            <Route path="/privacy" element={<Privacy />} />
            <Route path="/terms" element={<Terms />} />
            {/* Settings live inside ChatLayout (conversation sidebar on the
                left, settings on the right) — like Subscription. SettingsLayout
                supplies the in-panel header + tab nav. */}
            <Route path="/settings" element={<ChatLayout />}>
              <Route element={<SettingsLayout />}>
                <Route index element={<SettingsAccount />} />
                <Route path="account" element={<SettingsAccount />} />
                <Route path="appearance" element={<SettingsAppearance />} />
                <Route path="models" element={<SettingsModels />} />
                <Route path="personalization" element={<SettingsPersonalization />} />
                <Route path="privacy" element={<SettingsPrivacy />} />
                <Route path="shortcuts" element={<SettingsShortcuts />} />
                <Route path="about" element={<SettingsAbout />} />
              </Route>
            </Route>
            <Route path="/admin" element={<AdminLayout />}>
              <Route index element={<Navigate to="settings" replace />} />
              <Route path="channels" element={<AdminChannels />} />
              <Route path="models" element={<AdminModels />} />
              <Route path="models/:id" element={<AdminModelEdit />} />
              <Route path="model-tags" element={<AdminModelTags />} />
              <Route path="skills" element={<AdminSkills />} />
              <Route path="image-styles" element={<AdminImageStyles />} />
              <Route path="users" element={<AdminUsers />} />
              <Route path="user-groups" element={<AdminUserGroups />} />
              <Route path="workspaces" element={<AdminWorkspaces />} />
              <Route path="redeem-codes" element={<AdminRedeemCodes />} />
              <Route path="users/:id/conversations" element={<AdminUserConversations />} />
              <Route path="users/:id/library" element={<AdminUserLibrary />} />
              <Route path="users/:id/conversations/:cid" element={<AdminUserConversation />} />
              <Route path="usage" element={<AdminUsage />} />
              <Route path="analytics" element={<AdminAnalytics />} />
              <Route path="documents" element={<AdminDocuments />} />
              <Route path="files" element={<AdminFiles />} />
              <Route path="tools" element={<AdminTools />} />
              <Route path="audio" element={<AdminAudio />} />
              <Route path="oauth" element={<AdminOAuth />} />
              <Route path="moderation" element={<AdminModeration />} />
              <Route path="announcement" element={<AdminAnnouncement />} />
              <Route path="settings" element={<AdminSettings />} />
              <Route path="backup" element={<AdminBackup />} />
            </Route>
            <Route path="*" element={<NotFound />} />
          </Routes>
          {/* Overlay: settings modal rendered ON TOP of the live background page
              (only when opened over one). SettingsLayout is a portaled dialog, so
              it floats above whatever the main <Routes> rendered above. */}
          {backgroundLocation && (
            <Routes>
              <Route path="/settings" element={<SettingsLayout />}>
                <Route index element={<SettingsAccount />} />
                <Route path="account" element={<SettingsAccount />} />
                <Route path="appearance" element={<SettingsAppearance />} />
                <Route path="models" element={<SettingsModels />} />
                <Route path="personalization" element={<SettingsPersonalization />} />
                <Route path="privacy" element={<SettingsPrivacy />} />
                <Route path="shortcuts" element={<SettingsShortcuts />} />
                <Route path="about" element={<SettingsAbout />} />
              </Route>
            </Routes>
          )}
        </Suspense>
        <CommandMenu />
        <SetPasswordGate />
        <WelcomeCard />
        <AnnouncementPopup />
        <Toaster />
      </AuthGate>
    </TooltipProvider>
  )
}
