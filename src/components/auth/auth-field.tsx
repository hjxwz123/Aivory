import { useState, type InputHTMLAttributes, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, EyeOff } from 'lucide-react'
import { cn } from '@/lib/utils'

interface AuthFieldProps extends InputHTMLAttributes<HTMLInputElement> {
  id: string
  label: string
  error?: string
  description?: string
  headingAction?: ReactNode
}

/** Shared field geometry and focus treatment for every authentication step. */
export function AuthField({ id, label, error, description, headingAction, type = 'text', className, ...inputProps }: AuthFieldProps) {
  const { t } = useTranslation('auth')
  const [showPassword, setShowPassword] = useState(false)
  const password = type === 'password'
  const describedBy = [
    inputProps['aria-describedby'],
    error ? `${id}-error` : undefined,
    description ? `${id}-description` : undefined,
  ].filter(Boolean).join(' ') || undefined

  return (
    <div className="login-field">
      <div className="login-field-heading">
        <label htmlFor={id}>{label}</label>
        {headingAction}
      </div>
      <div className="login-input-wrap">
        <input
          {...inputProps}
          id={id}
          type={password && showPassword ? 'text' : type}
          className={cn('login-input', password && 'login-password', className)}
          aria-invalid={error ? true : inputProps['aria-invalid']}
          aria-describedby={describedBy}
        />
        {password ? (
          <button
            type="button"
            className="login-eye"
            disabled={inputProps.disabled}
            onClick={() => setShowPassword((value) => !value)}
            aria-label={t(showPassword ? 'fields.hidePassword' : 'fields.showPassword')}
            aria-pressed={showPassword}
          >
            {showPassword ? <EyeOff size={17} strokeWidth={1.6} aria-hidden /> : <Eye size={17} strokeWidth={1.6} aria-hidden />}
          </button>
        ) : null}
      </div>
      {description ? <span id={`${id}-description`} className="sr-only">{description}</span> : null}
      {error ? <p id={`${id}-error`} className="login-error login-field-error" role="alert">{error}</p> : null}
    </div>
  )
}
