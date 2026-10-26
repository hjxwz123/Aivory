import * as DialogPrimitive from '@radix-ui/react-dialog'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { useSurfaceViewport } from '@/hooks/use-app-viewport'

export type MobilePanelPresentation = 'fullscreen' | 'sheet'

/**
 * A touch form surface shared by dialogs and settings. Keep the Radix content
 * mounted when the layout changes, so rotating a phone cannot discard a draft.
 * Header/body/footer slots define the mobile scroll and action regions.
 */
export const MobilePanelSurface = forwardRef<
  ElementRef<typeof DialogPrimitive.Content>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & { mobilePresentation?: MobilePanelPresentation }
>(function MobilePanelSurface({ mobilePresentation, ...props }, ref) {
  const viewport = useSurfaceViewport()
  return <DialogPrimitive.Content
    ref={ref}
    data-mobile-panel={mobilePresentation}
    data-mobile-compact={mobilePresentation && viewport.height < 280 ? 'true' : undefined}
    {...props}
  />
})
