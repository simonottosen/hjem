import * as React from "react"
import * as TooltipPrimitive from "@radix-ui/react-tooltip"

import { cn } from "@/lib/utils"

function TooltipProvider({
  delayDuration = 0,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return <TooltipPrimitive.Provider delayDuration={delayDuration} {...props} />
}

function Tooltip({
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  const [open, setOpen] = React.useState(false)

  return (
    <TooltipPrimitive.Root
      open={open}
      onOpenChange={setOpen}
      {...props}
    >
      {React.Children.map(children, (child) => {
        if (React.isValidElement(child) && child.type === TooltipTrigger) {
          return React.cloneElement(
            child as React.ReactElement<{ onTouchOpenChange?: (open: boolean) => void }>,
            { onTouchOpenChange: setOpen }
          )
        }
        return child
      })}
    </TooltipPrimitive.Root>
  )
}

function TooltipTrigger({
  onTouchOpenChange,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Trigger> & {
  onTouchOpenChange?: (open: boolean) => void
}) {
  return (
    <TooltipPrimitive.Trigger
      onClick={(e) => {
        if (onTouchOpenChange && "ontouchstart" in window) {
          e.preventDefault()
          onTouchOpenChange(true)
        }
        props.onClick?.(e)
      }}
      onPointerDown={(e) => {
        // Prevent Radix from immediately closing on touch
        if (e.pointerType === "touch") {
          e.preventDefault()
        }
        props.onPointerDown?.(e)
      }}
      {...props}
    />
  )
}

function TooltipContent({
  className,
  sideOffset = 0,
  collisionPadding = 16,
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        sideOffset={sideOffset}
        collisionPadding={collisionPadding}
        className={cn(
          "z-50 w-fit rounded-md bg-foreground px-3 py-1.5 text-xs text-background animate-in fade-in-0 zoom-in-95",
          className
        )}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow className="z-50 size-2.5 fill-foreground" />
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  )
}

export { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider }
