import { useNavigate } from "@tanstack/react-router"
import { Dialog, DialogContent } from "@/components/ui/dialog"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Server, Image, Activity, Settings } from "lucide-react"

const routes = [
  { label: "Nodes", path: "/nodes", icon: Server },
  { label: "Images", path: "/images", icon: Image },
  { label: "Activity", path: "/activity", icon: Activity },
  { label: "Settings", path: "/settings", icon: Settings },
]

interface Props {
  open: boolean
  onClose: () => void
}

export function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()

  function select(path: string) {
    onClose()
    navigate({ to: path })
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="p-0 gap-0 max-w-md">
        <Command className="rounded-lg">
          <CommandInput placeholder="Go to..." />
          <CommandList>
            <CommandEmpty>No results.</CommandEmpty>
            <CommandGroup heading="Navigation">
              {routes.map((r) => (
                <CommandItem key={r.path} onSelect={() => select(r.path)}>
                  <r.icon className="mr-2 h-4 w-4" />
                  {r.label}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  )
}
