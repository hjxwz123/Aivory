import { lazy, Suspense, useEffect, useRef } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { useOpenSettings } from '@/hooks/use-open-settings'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/toaster'
import { CommandMenu } from '@/components/command-menu/command-menu'
import { WelcomeCard } from '@/components/welcome/welcome-card'
import { SetPasswordGate } from '@/components/welcome/set-password-gate'
import { AnnouncementPopup } from '@/components/announcement/announcement-popup'
import { AuthGate } from '@/components/auth/auth-gate'
import SettingsDialog from '@/pages/settings/SettingsLayout'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { useSettings } from '@/store/settings'
import { useConversations } from '@/store/conversations'
import { useUI } from '@/store/ui'
import { useSettingsModal } from '@/store/settings-modal'
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

// Mobile: any navigation dismisses the sidebar drawer. Individual links inside
// the drawer already close it on tap for instant feedback, but plenty of
// navigations bypass those handlers (user-menu items, the archived dialog, the
// command menu, workspace switches) — this catches them all, and also resets a
// stale open flag when returning from layouts without the drawer (e.g. /admin).
function CloseNavOnNavigate() {
  const { pathname, search } = useLocation()
  const setNavOpen = useUI((s) => s.setNavOpen)
  useEffect(() => {
    setNavOpen(false)
  }, [pathname, search, setNavOpen])
  return null
}

// The settings dialog is pure UI state, so a route change underneath (browser
// Back, sign-out redirects, command-menu navigation) must dismiss it or it
// would float over the new page. Lives HERE — outside AuthGate — because the
// sign-out flow remounts the AuthGate subtree, which would wipe a ref kept
// inside the dialog; App never remounts, so the previous-path ref survives.
// The /settings deep-link redirect is exempt: that navigation is the one that
// OPENS the dialog (SettingsRedirect below).
function CloseSettingsOnNavigate() {
  const { pathname } = useLocation()
  const prevPathRef = useRef(pathname)
  useEffect(() => {
    const prev = prevPathRef.current
    prevPathRef.current = pathname
    if (prev !== pathname && !prev.startsWith('/settings')) {
      useSettingsModal.getState().close()
    }
  }, [pathname])
  return null
}

// Settings is a pure-state dialog (§设置-去路由化) — this route shim keeps the
// old /settings/:tab URLs working: the OAuth identity-link callback redirects
// to /settings/account?linked=…, and older bookmarks/deep links still point
// here. It opens the dialog on the requested tab and replaces the URL with
// /chat, carrying the query string so the account tab's ?linked toast fires.
function SettingsRedirect() {
  const { tab } = useParams<{ tab?: string }>()
  const { search } = useLocation()
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  useEffect(() => {
    openSettings(tab)
    navigate({ pathname: '/chat', search }, { replace: true })
  }, [tab, search, navigate, openSettings])
  return <RouteFallback />
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
  // A route navigation is a safe, invisible moment to apply a pending upgrade
  // (never fires while a message is streaming — see maybeApplyUpdate). The
  // settings dialog no longer navigates (pure UI state), so every pathname
  // change here is a real page swap the user already perceives as a jump.
  useEffect(() => {
    maybeApplyUpdate('navigation')
  }, [location.pathname])

  return (
    <TooltipProvider delayDuration={280} skipDelayDuration={120}>
      <ScrollToTop />
      <CloseNavOnNavigate />
      <CloseSettingsOnNavigate />
      <AuthGate>
        <GlobalShortcuts />
        <Suspense fallback={<RouteFallback />}>
          <Routes>
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
            {/* Settings is a dialog, not a page — these only catch old
                /settings/:tab links and the OAuth ?linked callback, opening
                the dialog and replacing the URL (see SettingsRedirect). */}
            <Route path="/settings" element={<SettingsRedirect />} />
            <Route path="/settings/:tab" element={<SettingsRedirect />} />
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
        </Suspense>
        {/* Portaled dialog floating above whatever the Routes rendered; renders
            nothing while closed (tab chunks stay unloaded until first open). */}
        <SettingsDialog />
        <CommandMenu />
        <SetPasswordGate />
        <WelcomeCard />
        <AnnouncementPopup />
        <Toaster />
      </AuthGate>
    </TooltipProvider>
  )
}
