import { lazy, Suspense, useState, type ComponentType } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { ModelQuotaEditor } from '@/components/admin/model-quota-editor'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/store/auth'
import i18n from '@/i18n'
import '@/i18n/admin-resources'
import '@/styles/globals.css'

const params = new URLSearchParams(location.search)
const view = params.get('view') ?? 'users'
const pages = import.meta.glob('../../src/pages/admin/Admin*.tsx')
const pageName = {
  users: 'AdminUsers', channels: 'AdminChannels', models: 'AdminModels', 'model-edit': 'AdminModelEdit', prompts: 'AdminPrompts',
  skills: 'AdminSkills', oauth: 'AdminOAuth', tags: 'AdminModelTags', groups: 'AdminUserGroups',
  'payment-channels': 'AdminPaymentChannels', 'payment-methods': 'AdminPaymentMethods',
  credits: 'AdminCreditSettings', redeem: 'AdminRedeemCodes', mcp: 'AdminMCP',
  feedback: 'AdminFeedback', 'model-feedback': 'AdminModelFeedback', resources: 'AdminResources',
  workspaces: 'AdminWorkspaces', domains: 'AdminDomains', orders: 'AdminPaymentOrders',
  previews: 'AdminHTMLPreviews', logins: 'AdminUserLoginHistory', memories: 'AdminUserMemories',
  conversations: 'AdminUserConversations', library: 'AdminUserLibrary', files: 'AdminFiles', usage: 'AdminUsage', audit: 'AdminAuditLogs', analytics: 'AdminAnalytics',
}[view]
const Page = pageName ? lazy(async () => {
  if (view === 'model-feedback') {
    const { AdminModelFeedback } = await import('@/pages/admin/AdminModelFeedback')
    return { default: () => <AdminModelFeedback days={30} /> }
  }
  return (pages[`../../src/pages/admin/${pageName}.tsx`] as () => Promise<{ default: ComponentType }>)()
}) : null
const ConversationDetail = lazy(() => import('@/pages/admin/AdminUserConversation'))

function SortingCase() {
  const [items, setItems] = useState([{ id: 'a', name: 'Alpha' }, { id: 'b', name: 'Beta' }, { id: 'c', name: 'Gamma' }])
  return <AdminSortableList items={items} onItemsChange={setItems} onOrderCommit={(next) => Reflect.set(window, '__ADMIN_ORDER__', next.map((item) => item.id))} dragHandleLabel="Drag" moveUpLabel="Up" moveDownLabel="Down" columns={[{ id: 'name', header: 'Name', width: 260, render: (item) => item.name }, { id: 'actions', header: 'Actions', width: 100, align: 'right', render: (item) => <Button variant="ghost" size="sm" onClick={() => Reflect.set(window, '__ADMIN_ACTION__', item.id)}>Open</Button> }]} />
}

useAuth.setState({ user: { id: 'u1', name: 'AIVORY', email: 'admin@example.com', role: 'admin', status: 'active', group_id: 'g1', created_at: 1, settings: {} }, status: 'authenticated' })
document.documentElement.classList.toggle('dark', params.get('theme') !== 'light')
document.documentElement.style.setProperty('--accent-hue', '250')
await i18n.changeLanguage(params.get('lang') ?? 'zh')

createRoot(document.getElementById('root')!).render(
  <TooltipProvider>
    <MemoryRouter initialEntries={[view === 'model-edit' ? `/admin/models/${params.get('model') ?? 'm1'}` : view === 'users' ? '/admin/users' : '/admin/users/u1']}>
      <main style={{ maxWidth: 1200, margin: '0 auto', padding: 16, minWidth: 0 }}>
        <Suspense fallback={<p>Loading</p>}>
          <Routes>
            <Route path={view === 'model-edit' ? '/admin/models/:id' : view === 'users' ? '/admin/users' : '/admin/users/:id/*'} element={view === 'quota' ? <ModelQuotaEditor modelId="m1" /> : Page ? <Page /> : <SortingCase />} />
            {view === 'users' ? <Route path="/admin/users/:id/conversations/:cid" element={<ConversationDetail />} /> : null}
          </Routes>
        </Suspense>
      </main>
    </MemoryRouter>
  </TooltipProvider>,
)
