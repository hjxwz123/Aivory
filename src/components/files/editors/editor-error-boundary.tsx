import { Component, Fragment, type ErrorInfo, type ReactNode } from 'react'
import { FileWarning, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'

interface EditorErrorBoundaryProps {
  /** Human-readable document name, echoed in the notice so the pane is identifiable. */
  name: string
  /** Message shown when the child subtree throws. */
  title: string
  retryLabel: string
  children: ReactNode
}

interface EditorErrorBoundaryState {
  error: Error | null
  /** Bumped by "try again" so the subtree is rebuilt from scratch. */
  revision: number
}

/**
 * Keeps a throwing editor (or preview) from taking the whole panel down.
 *
 * Without this, a render-time throw inside a lazily loaded editor unmounts the
 * whole React tree above it, which is exactly what a user reports as "clicking
 * Edit shows a blank screen": no toolbar, no message, nothing to act on. Here it
 * degrades to a visible, actionable notice and can rebuild the surface.
 *
 * It is a class because React has no hook equivalent for error boundaries.
 */
export class EditorErrorBoundary extends Component<EditorErrorBoundaryProps, EditorErrorBoundaryState> {
  state: EditorErrorBoundaryState = { error: null, revision: 0 }

  static getDerivedStateFromError(error: Error): Partial<EditorErrorBoundaryState> {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface the failure in the console for bug reports: the visible notice
    // deliberately stays short, but the stack must remain recoverable.
    console.error('[artifact-panel] editor surface crashed', error, info.componentStack)
  }

  private retry = () => {
    this.setState((state) => ({ error: null, revision: state.revision + 1 }))
  }

  render() {
    const { error, revision } = this.state
    const { name, title, retryLabel, children } = this.props

    if (!error) {
      // Keyed by revision so "try again" rebuilds the subtree instead of
      // re-rendering the instance that just threw. A Fragment keeps the child's
      // flex sizing intact (no extra wrapper box in the column).
      return <Fragment key={revision}>{children}</Fragment>
    }

    return (
      <div className="flex h-full min-h-[20rem] items-center justify-center p-6" role="alert">
        <div className="max-w-sm text-center">
          <span className="mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-danger-soft)] text-[var(--color-danger)]">
            <FileWarning size={21} aria-hidden />
          </span>
          <p className="mt-4 text-sm font-medium text-[var(--color-fg)]">{title}</p>
          <p className="mt-1 break-words text-xs leading-relaxed text-[var(--color-fg-muted)]">{name}</p>
          <Button
            variant="secondary"
            size="sm"
            className="mt-4"
            leadingIcon={<RefreshCw size={14} aria-hidden />}
            onClick={this.retry}
          >
            {retryLabel}
          </Button>
        </div>
      </div>
    )
  }
}
