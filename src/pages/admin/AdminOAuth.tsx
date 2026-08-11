/**
 * AdminOAuth — configure social / OAuth login providers. Built-in kinds
 * (Google, GitHub, Apple) only need client credentials; generic OAuth2 and OIDC
 * providers take custom authorize / token endpoints. OAuth2 also takes a
 * UserInfo endpoint, while OIDC requires issuer and JWKS metadata. The
 * client_secret is write-only — it's never returned, and an empty field on edit
 * keeps the saved value.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2, Copy, Check, RefreshCw } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
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
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import {
  getOAuthProviderFormCapabilities,
  oauthProviderErrorTranslationKey,
  OAUTH_PROVIDER_KINDS,
} from '@/lib/oauth'

type Editable = Partial<ApiOAuthProvider> & { client_secret?: string }

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
  const [preparingRedirect, setPreparingRedirect] = useState(false)
  const [prepareError, setPrepareError] = useState('')
  const prepareVersionRef = useRef(0)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)

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

  async function prepareNewProvider() {
    const version = ++prepareVersionRef.current
    setPreparingRedirect(true)
    setPrepareError('')
    try {
      const prepared = await adminApi.prepareOAuthProvider()
      if (prepareVersionRef.current !== version) return
      setEditor((current) => current.open && !current.row
        ? { ...current, draft: { ...current.draft, id: prepared.id, redirect_uri: prepared.redirect_uri } }
        : current)
    } catch (error) {
      if (prepareVersionRef.current !== version) return
      setPrepareError(error instanceof ApiError ? error.message : t('admin:oauth.errors.callbackLoadFailed'))
    } finally {
      if (prepareVersionRef.current === version) setPreparingRedirect(false)
    }
  }

  function openNew() {
    setCopied(false)
    setPrepareError('')
    setEditor({ open: true, draft: { kind: 'google', enabled: true, name: 'Google', sort_order: rows.length } })
    void prepareNewProvider()
  }
  function openEdit(row: ApiOAuthProvider) {
    prepareVersionRef.current += 1
    setPreparingRedirect(false)
    setPrepareError('')
    setCopied(false)
    setEditor({ open: true, row, draft: { ...row, client_secret: '' } })
  }

  function setDraft(patch: Partial<Editable>) {
    setEditor((ed) => ({ ...ed, draft: { ...ed.draft, ...patch } }))
  }

  function setOrderedRows(next: ApiOAuthProvider[]) {
    setRows(next.map((row, sortOrder) => ({ ...row, sort_order: sortOrder })))
  }

  function persistOrder(next: ApiOAuthProvider[], previous: ApiOAuthProvider[]) {
    void adminApi.reorderOAuthProviders(next.map((row) => row.id)).catch((error) => {
      setRows(previous)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.reorderFailed'))
    })
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name?.trim()) {
      toast.error(t('admin:oauth.errors.nameRequired'))
      return
    }
    if (!editor.row && (!d.id || !d.redirect_uri)) {
      toast.error(t('admin:oauth.errors.callbackLoadFailed'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      const payload = { ...d }
      delete payload.redirect_uri
      if (editor.row) {
        await adminApi.updateOAuthProvider(editor.row.id, payload)
        toast.success(t('admin:oauth.updated'))
      } else {
        await adminApi.createOAuthProvider(payload)
        toast.success(t('admin:oauth.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      const oauthErrorKey = e instanceof ApiError ? oauthProviderErrorTranslationKey(e.message) : null
      if (oauthErrorKey) {
        toast.error(t(oauthErrorKey))
      } else if (e instanceof ApiError && e.status === 409) {
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
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeOAuthProvider(row.id)
      toast.success(t('admin:oauth.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function copyRedirect() {
    const redirectURI = editor.draft.redirect_uri
    if (!redirectURI) return
    try {
      await navigator.clipboard.writeText(redirectURI)
      setCopied(true)
      setTimeout(() => setCopied(false), 1800)
    } catch {
      toast.error(t('admin:common.failed'))
    }
  }

  const kind = editor.draft.kind ?? 'google'
  const {
    usesAppleCredentials: isApple,
    usesCustomIcon,
    showsCustomEndpoints,
    showsOidcMetadata,
    showsUserInfoEndpoint,
  } = getOAuthProviderFormCapabilities(kind)

  return (
    <div>
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:oauth.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:oauth.lead')}</p>
        </div>
        <Button className="min-h-[var(--tap-min)] w-full sm:min-h-0 sm:w-auto" leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
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
          <AdminSortableList
            items={rows}
            onItemsChange={setOrderedRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            mobileDragOnly
            rowClassName="grid grid-cols-[2.75rem_auto_minmax(0,1fr)] items-center gap-x-2 gap-y-2 px-2 py-3.5 md:grid-cols-[auto_auto_auto_minmax(0,1fr)_auto_auto] md:gap-3 md:px-5 md:py-4"
            renderItem={(p) => (
              <>
                <div className="col-start-2 row-start-1 inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg)] md:col-start-auto md:row-start-auto">
                  <OAuthBrandGlyph kind={p.kind} icon={p.icon} size={18} />
                </div>
                <div className="col-start-3 row-start-1 min-w-0 md:col-start-auto md:row-start-auto">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{p.name}</span>
                    <Badge size="xs">{t(`admin:oauth.kinds.${p.kind}`)}</Badge>
                    {p.enabled ? null : <Badge size="xs" variant="neutral">{t('admin:channels.labels.disabled')}</Badge>}
                  </div>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                    {p.client_id || t('admin:oauth.noClientId')} · {p.has_secret ? t('admin:channels.labels.keySet') : t('admin:channels.labels.noKey')}
                  </div>
                </div>
                <div className="col-span-3 row-start-2 flex items-center justify-end gap-1 md:contents">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                    aria-label={`${t('admin:common.edit')}: ${p.name}`}
                    leadingIcon={<Pencil size={13} aria-hidden />}
                    onClick={() => openEdit(p)}
                  >
                    <span className="max-md:sr-only">{t('admin:common.edit')}</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                    aria-label={`${t('admin:common.remove')}: ${p.name}`}
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => setConfirmDelete(p)}
                  >
                    <span className="max-md:sr-only">{t('admin:common.remove')}</span>
                  </Button>
                </div>
              </>
            )}
          />
        )}
      </section>

      <Dialog
        open={editor.open}
        onOpenChange={(open) => {
          if (savingRef.current) return
          if (!open) {
            prepareVersionRef.current += 1
            setPreparingRedirect(false)
            setPrepareError('')
          }
          setEditor((current) => ({ ...current, open }))
        }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:oauth.editorTitle') : t('admin:oauth.newTitle')}</DialogTitle>
            <DialogDescription>{t(`admin:oauth.hints.${kind}`)}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field label={t('admin:oauth.fields.kind')} htmlFor="oa-kind">
                  <Select value={kind} onValueChange={(v) => setDraft({ kind: v as OAuthKind })}>
                    <SelectTrigger id="oa-kind">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {OAUTH_PROVIDER_KINDS.map((k) => (
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
                  {editor.draft.redirect_uri ? (
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
                {editor.draft.redirect_uri ? (
                  <Input
                    readOnly
                    value={editor.draft.redirect_uri}
                    onFocus={(e) => e.currentTarget.select()}
                    className="mt-2 font-mono text-[12px]"
                  />
                ) : preparingRedirect ? (
                  <div className="mt-2 h-10 animate-pulse rounded-[8px] bg-[var(--color-surface)]" role="status">
                    <span className="sr-only">{t('common:common.loading')}</span>
                  </div>
                ) : (
                  <div className="mt-2 flex items-center justify-between gap-3 rounded-[8px] bg-[var(--color-danger-soft)] px-3 py-2 text-[12px] text-[var(--color-danger)]">
                    <span>{prepareError || t('admin:oauth.errors.callbackLoadFailed')}</span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="xs"
                      className="shrink-0"
                      leadingIcon={<RefreshCw size={12} aria-hidden />}
                      onClick={() => void prepareNewProvider()}
                    >
                      {t('common:actions.tryAgain')}
                    </Button>
                  </div>
                )}
                <p className="mt-2 text-[12px] text-[var(--color-fg-subtle)] leading-relaxed">
                  {t('admin:oauth.fields.redirectUriHint')}
                </p>
              </div>

              {usesCustomIcon ? (
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
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
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

              {showsCustomEndpoints ? (
                <>
                  {showsOidcMetadata ? (
                    <>
                      <Field label={t('admin:oauth.fields.issuerUrl')} htmlFor="oa-issuer">
                        <Input
                          id="oa-issuer"
                          value={editor.draft.issuer_url ?? ''}
                          onChange={(e) => setDraft({ issuer_url: e.target.value })}
                          placeholder="https://id.example.com"
                        />
                      </Field>
                      <Field label={t('admin:oauth.fields.jwksUrl')} htmlFor="oa-jwks">
                        <Input
                          id="oa-jwks"
                          value={editor.draft.jwks_url ?? ''}
                          onChange={(e) => setDraft({ jwks_url: e.target.value })}
                          placeholder="https://id.example.com/.well-known/jwks.json"
                        />
                      </Field>
                    </>
                  ) : null}
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
                  {showsUserInfoEndpoint ? (
                    <Field label={t('admin:oauth.fields.userinfoUrl')} htmlFor="oa-userinfo">
                      <Input
                        id="oa-userinfo"
                        value={editor.draft.userinfo_url ?? ''}
                        onChange={(e) => setDraft({ userinfo_url: e.target.value })}
                        placeholder="https://id.example.com/userinfo"
                      />
                    </Field>
                  ) : null}
                  <Field
                    label={t('admin:oauth.fields.scopes')}
                    htmlFor="oa-scopes"
                    hint={t(showsOidcMetadata
                      ? 'admin:oauth.fields.scopesHintOidc'
                      : 'admin:oauth.fields.scopesHintOauth2')}
                  >
                    <Input
                      id="oa-scopes"
                      value={editor.draft.scopes ?? ''}
                      onChange={(e) => setDraft({ scopes: e.target.value })}
                      placeholder={showsOidcMetadata ? 'openid email profile' : 'email profile'}
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
            <Button
              loading={saving}
              disabled={!editor.row && (preparingRedirect || !editor.draft.redirect_uri)}
              onClick={() => void submit()}
            >
              {t('common:actions.save')}
            </Button>
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
            <Button variant="ghost" disabled={deleting} onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
