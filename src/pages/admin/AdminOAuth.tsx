/**
 * AdminOAuth — configure social / OAuth login providers. Built-in kinds
 * (Google, GitHub, Apple) only need client credentials; the generic OIDC kind
 * also takes the authorize / token / userinfo endpoints. The client_secret is
 * write-only — it's never returned, and an empty field on edit keeps the saved
 * value (mirrors the channel api_key policy).
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2, Copy, Check } from 'lucide-react'
import { adminApi, ApiError, apiUrl } from '@/api'
import type { ApiOAuthProvider, OAuthKind } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { Badge } from '@/components/ui/badge'
import { IconUploader } from '@/components/admin/icon-uploader'
import { OAuthBrandGlyph } from '@/components/auth/oauth-glyph'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Editable = Partial<ApiOAuthProvider> & { client_secret?: string }

const KINDS: OAuthKind[] = ['google', 'github', 'apple', 'oidc']

// Build the redirect URI an admin must register in the provider console. Uses
// the same API base the app talks to, resolved to an absolute URL.
function redirectUriFor(id: string): string {
  const cb = apiUrl(`/auth/oauth/${id}/callback`)
  return cb.startsWith('http') ? cb : window.location.origin + cb
}

export default function AdminOAuth() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiOAuthProvider[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiOAuthProvider; draft: Editable }>({
    open: false,
    draft: { kind: 'google', enabled: true },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiOAuthProvider | null>(null)
  const [copied, setCopied] = useState(false)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.oauthProviders())
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setCopied(false)
    setEditor({ open: true, draft: { kind: 'google', enabled: true, name: 'Google' } })
  }
  function openEdit(row: ApiOAuthProvider) {
    setCopied(false)
    setEditor({ open: true, row, draft: { ...row, client_secret: '' } })
  }

  function setDraft(patch: Partial<Editable>) {
    setEditor((ed) => ({ ...ed, draft: { ...ed.draft, ...patch } }))
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name?.trim()) {
      toast.error(t('admin:oauth.errors.nameRequired'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateOAuthProvider(editor.row.id, d)
        toast.success(t('admin:oauth.updated'))
      } else {
        await adminApi.createOAuthProvider(d)
        toast.success(t('admin:oauth.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(row: ApiOAuthProvider) {
    try {
      await adminApi.removeOAuthProvider(row.id)
      toast.success(t('admin:oauth.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  async function copyRedirect() {
    if (!editor.row) return
    try {
      await navigator.clipboard.writeText(redirectUriFor(editor.row.id))
      setCopied(true)
      setTimeout(() => setCopied(false), 1800)
    } catch {
      /* clipboard blocked — the field is selectable as a fallback */
    }
  }

  const kind = editor.draft.kind ?? 'google'
  const isApple = kind === 'apple'
  const isOidc = kind === 'oidc'

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:oauth.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:oauth.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:oauth.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:oauth.empty')}
          </div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((p) => (
              <li key={p.id} className="grid grid-cols-[auto_1fr_auto_auto] items-center gap-3 px-5 py-4">
                <div className="shrink-0 size-9 inline-flex items-center justify-center rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]">
                  <OAuthBrandGlyph kind={p.kind} icon={p.icon} size={18} />
                </div>
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{p.name}</span>
                    <Badge size="xs">{t(`admin:oauth.kinds.${p.kind}`)}</Badge>
                    {p.enabled ? null : <Badge size="xs" variant="neutral">{t('admin:channels.labels.disabled')}</Badge>}
                  </div>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                    {p.client_id || t('admin:oauth.noClientId')} · {p.has_secret ? t('admin:channels.labels.keySet') : t('admin:channels.labels.noKey')}
                  </div>
                </div>
                <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(p)}>
                  {t('admin:common.edit')}
                </Button>
                <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={13} aria-hidden />} onClick={() => setConfirmDelete(p)}>
                  {t('admin:common.remove')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:oauth.editorTitle') : t('admin:oauth.newTitle')}</DialogTitle>
            <DialogDescription>{t(`admin:oauth.hints.${kind}`)}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <div className="grid grid-cols-2 gap-4">
                <Field label={t('admin:oauth.fields.kind')} htmlFor="oa-kind">
                  <Select value={kind} onValueChange={(v) => setDraft({ kind: v as OAuthKind })}>
                    <SelectTrigger id="oa-kind">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {KINDS.map((k) => (
                        <SelectItem key={k} value={k}>
                          {t(`admin:oauth.kinds.${k}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('admin:oauth.fields.name')} htmlFor="oa-name">
                  <Input
                    id="oa-name"
                    value={editor.draft.name ?? ''}
                    onChange={(e) => setDraft({ name: e.target.value })}
                    placeholder="Google"
                  />
                </Field>
              </div>

              {/* Callback/redirect URI — the value an admin must register in the
                  provider console. Hoisted to the top of the form (and styled as
                  a callout) because it's the first thing they go looking for. */}
              <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-3.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium text-[var(--color-fg)]">
                    {t('admin:oauth.fields.redirectUri')}
                  </span>
                  {editor.row ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      leadingIcon={copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
                      onClick={() => void copyRedirect()}
                    >
                      {copied ? t('admin:oauth.copied') : t('admin:oauth.copy')}
                    </Button>
                  ) : null}
                </div>
                {editor.row ? (
                  <Input
                    readOnly
                    value={redirectUriFor(editor.row.id)}
                    onFocus={(e) => e.currentTarget.select()}
                    className="mt-2 font-mono text-[12px]"
                  />
                ) : (
                  <p className="mt-1.5 text-[12px] text-[var(--color-fg-subtle)] leading-relaxed">
                    {t('admin:oauth.fields.redirectUriNew')}
                  </p>
                )}
                <p className="mt-2 text-[12px] text-[var(--color-fg-subtle)] leading-relaxed">
                  {t('admin:oauth.fields.redirectUriHint')}
                </p>
              </div>

              {isOidc ? (
                <Field label={t('admin:oauth.fields.icon')}>
                  <IconUploader
                    value={editor.draft.icon ?? ''}
                    onChange={(v) => setDraft({ icon: v })}
                    placeholder={t('admin:oauth.fields.iconPlaceholder')}
                  />
                </Field>
              ) : (
                <Field label={t('admin:oauth.fields.icon')} hint={t('admin:oauth.fields.iconBuiltin')}>
                  <div className="inline-flex items-center gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2 text-[var(--color-fg)]">
                    <OAuthBrandGlyph kind={kind} size={18} />
                    <span className="text-sm">{t(`admin:oauth.kinds.${kind}`)}</span>
                  </div>
                </Field>
              )}

              <Field label={t('admin:oauth.fields.clientId')} htmlFor="oa-cid">
                <Input
                  id="oa-cid"
                  value={editor.draft.client_id ?? ''}
                  onChange={(e) => setDraft({ client_id: e.target.value })}
                  placeholder={isApple ? 'com.example.app (Services ID)' : '…'}
                />
              </Field>

              <Field
                label={isApple ? t('admin:oauth.fields.clientSecretApple') : t('admin:oauth.fields.clientSecret')}
                htmlFor="oa-secret"
                hint={editor.row ? t('admin:oauth.fields.clientSecretHintEdit') : undefined}
              >
                {isApple ? (
                  <Textarea
                    id="oa-secret"
                    rows={5}
                    value={editor.draft.client_secret ?? ''}
                    onChange={(e) => setDraft({ client_secret: e.target.value })}
                    placeholder={'-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----'}
                    className="font-mono text-[12px]"
                  />
                ) : (
                  <Input
                    id="oa-secret"
                    type="password"
                    value={editor.draft.client_secret ?? ''}
                    onChange={(e) => setDraft({ client_secret: e.target.value })}
                    placeholder="••••••••"
                  />
                )}
              </Field>

              {isApple ? (
                <div className="grid grid-cols-2 gap-4">
                  <Field label={t('admin:oauth.fields.teamId')} htmlFor="oa-team">
                    <Input
                      id="oa-team"
                      value={editor.draft.team_id ?? ''}
                      onChange={(e) => setDraft({ team_id: e.target.value })}
                      placeholder="ABCDE12345"
                    />
                  </Field>
                  <Field label={t('admin:oauth.fields.keyId')} htmlFor="oa-key">
                    <Input
                      id="oa-key"
                      value={editor.draft.key_id ?? ''}
                      onChange={(e) => setDraft({ key_id: e.target.value })}
                      placeholder="XYZ123ABCD"
                    />
                  </Field>
                </div>
              ) : null}

              {isOidc ? (
                <>
                  <Field label={t('admin:oauth.fields.authUrl')} htmlFor="oa-auth">
                    <Input
                      id="oa-auth"
                      value={editor.draft.auth_url ?? ''}
                      onChange={(e) => setDraft({ auth_url: e.target.value })}
                      placeholder="https://id.example.com/authorize"
                    />
                  </Field>
                  <Field label={t('admin:oauth.fields.tokenUrl')} htmlFor="oa-token">
                    <Input
                      id="oa-token"
                      value={editor.draft.token_url ?? ''}
                      onChange={(e) => setDraft({ token_url: e.target.value })}
                      placeholder="https://id.example.com/token"
                    />
                  </Field>
                  <Field label={t('admin:oauth.fields.userinfoUrl')} htmlFor="oa-userinfo">
                    <Input
                      id="oa-userinfo"
                      value={editor.draft.userinfo_url ?? ''}
                      onChange={(e) => setDraft({ userinfo_url: e.target.value })}
                      placeholder="https://id.example.com/userinfo"
                    />
                  </Field>
                  <Field label={t('admin:oauth.fields.scopes')} htmlFor="oa-scopes" hint={t('admin:oauth.fields.scopesHint')}>
                    <Input
                      id="oa-scopes"
                      value={editor.draft.scopes ?? ''}
                      onChange={(e) => setDraft({ scopes: e.target.value })}
                      placeholder="openid email profile"
                    />
                  </Field>
                </>
              ) : null}

              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                <span className="text-sm text-[var(--color-fg)]">{t('admin:oauth.fields.enabled')}</span>
                <Switch
                  checked={editor.draft.enabled ?? true}
                  onCheckedChange={(v) => setDraft({ enabled: v })}
                />
              </label>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" disabled={saving} onClick={() => setEditor({ ...editor, open: false })}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>{t('common:actions.save')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:oauth.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:oauth.removeBody', { name: confirmDelete.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
