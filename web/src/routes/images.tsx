import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, ChevronUp, ChevronDown, ChevronsUpDown, Copy, Check } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet"
import { StatusDot } from "@/components/StatusDot"
import { apiFetch } from "@/lib/api"
import type { BaseImage, ListImagesResponse } from "@/lib/types"
import { cn } from "@/lib/utils"

interface ImageSearch {
  q?: string
  tab?: string
  sort?: string
  dir?: "asc" | "desc"
}

function imageStateLabel(status: string): string {
  switch (status) {
    case "ready": return "ready"
    case "building": return "building"
    case "error": return "error"
    case "archived": return "archived"
    case "interrupted": return "interrupted"
    default: return status
  }
}

function imageState(status: string): "healthy" | "warning" | "error" | "neutral" | "pending" {
  switch (status) {
    case "ready": return "healthy"
    case "building": return "pending"
    case "error": return "error"
    case "archived": return "neutral"
    default: return "neutral"
  }
}

function formatBytes(bytes: number): string {
  if (!bytes) return "—"
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function ImagesPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as ImageSearch

  const q = search.q ?? ""
  const tab = search.tab ?? "base"
  const sortCol = search.sort ?? ""
  const sortDir = search.dir ?? "asc"
  const [advanced, setAdvanced] = React.useState(false)
  const [selectedImage, setSelectedImage] = React.useState<BaseImage | null>(null)

  function updateSearch(patch: Partial<ImageSearch>) {
    navigate({
      to: "/images",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        tab: patch.tab !== undefined ? patch.tab : tab === "base" ? undefined : tab,
        sort: patch.sort !== undefined ? patch.sort : sortCol || undefined,
        dir: patch.dir !== undefined ? patch.dir : sortDir === "asc" ? undefined : "desc",
      },
      replace: true,
    })
  }

  const { data } = useQuery<ListImagesResponse>({
    queryKey: ["images", q, sortCol, sortDir],
    queryFn: () => {
      const params = new URLSearchParams()
      if (q) params.set("search", q)
      if (sortCol) params.set("sort", sortCol)
      if (sortDir) params.set("dir", sortDir)
      return apiFetch<ListImagesResponse>(`/api/v1/images?${params}`)
    },
    refetchInterval: 15000,
    staleTime: 10000,
  })

  const allImages = data?.images ?? []
  const baseImages = allImages.filter((img) => !img.tags?.includes("bundle"))
  const bundles = allImages.filter((img) => img.tags?.includes("bundle"))

  function handleSort(col: string) {
    if (sortCol === col) {
      updateSearch({ dir: sortDir === "asc" ? "desc" : "asc" })
    } else {
      updateSearch({ sort: col, dir: "asc" })
    }
  }

  function SortIcon({ col }: { col: string }) {
    if (sortCol !== col) return <ChevronsUpDown className="h-3 w-3 opacity-40" />
    return sortDir === "asc" ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />
  }

  function relativeTime(iso?: string) {
    if (!iso) return "—"
    try { return formatDistanceToNow(new Date(iso), { addSuffix: true }) } catch { return "—" }
  }

  const displayImages = tab === "bundles" ? bundles : baseImages

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center justify-between gap-3 border-b border-border px-6 py-3">
        <div className="relative w-72">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            className="pl-8"
            placeholder="Search images..."
            value={q}
            onChange={(e) => updateSearch({ q: e.target.value || undefined })}
          />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setAdvanced((a) => !a)}
          className={cn(advanced && "bg-secondary")}
        >
          {advanced ? "Basic view" : "Advanced"}
        </Button>
      </div>

      {/* Tabs */}
      <div className="flex-1 overflow-auto">
        <Tabs
          value={tab}
          onValueChange={(v) => updateSearch({ tab: v === "base" ? undefined : v })}
          className="flex flex-col h-full"
        >
          <div className="px-6 pt-3 border-b border-border shrink-0">
            <TabsList>
              <TabsTrigger value="base">Base Images ({baseImages.length})</TabsTrigger>
              <TabsTrigger value="bundles">Bundles ({bundles.length})</TabsTrigger>
            </TabsList>
          </div>

          <TabsContent value={tab} className="flex-1 overflow-auto mt-0">
            {displayImages.length === 0 ? (
              <EmptyState />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>
                      <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("name")}>
                        Name <SortIcon col="name" />
                      </button>
                    </TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>
                      <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("version")}>
                        Version <SortIcon col="version" />
                      </button>
                    </TableHead>
                    <TableHead>Size</TableHead>
                    <TableHead>SHA256</TableHead>
                    {advanced && <TableHead>OS / Arch</TableHead>}
                    <TableHead>
                      <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("created_at")}>
                        Created <SortIcon col="created_at" />
                      </button>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {displayImages.map((img) => (
                    <TableRow key={img.id} className="cursor-pointer" onClick={() => setSelectedImage(img)}>
                      <TableCell>
                        <span className="font-medium text-sm">{img.name}</span>
                      </TableCell>
                      <TableCell>
                        <StatusDot state={imageState(img.status)} label={imageStateLabel(img.status)} />
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {img.version || "—"}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatBytes(img.size_bytes)}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {img.checksum ? img.checksum.slice(0, 12) + "…" : "—"}
                      </TableCell>
                      {advanced && (
                        <TableCell className="text-xs text-muted-foreground">
                          {[img.os, img.arch].filter(Boolean).join(" / ") || "—"}
                        </TableCell>
                      )}
                      <TableCell className="text-xs text-muted-foreground">
                        {relativeTime(img.created_at)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </TabsContent>
        </Tabs>
      </div>

      {selectedImage && (
        <ImageSheet image={selectedImage} onClose={() => setSelectedImage(null)} relativeTime={relativeTime} />
      )}
    </div>
  )
}

function EmptyState() {
  const [copied, setCopied] = React.useState(false)
  const snippet = "clustr-serverd image upload --name myimage --version 1.0 /path/to/image.tar"

  function copy() {
    navigator.clipboard.writeText(snippet).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-4 p-8 text-center">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">No images yet</h2>
        <p className="text-sm text-muted-foreground">Upload a base image to get started:</p>
      </div>
      <div className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 max-w-xl">
        <code className="text-xs font-mono flex-1 text-left">{snippet}</code>
        <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0" onClick={copy}>
          {copied ? <Check className="h-3.5 w-3.5 text-green-500" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
    </div>
  )
}

function ImageSheet({ image, onClose, relativeTime }: { image: BaseImage; onClose: () => void; relativeTime: (iso?: string) => string }) {
  const [copiedSha, setCopiedSha] = React.useState(false)

  function copySHA() {
    navigator.clipboard.writeText(image.checksum).then(() => {
      setCopiedSha(true)
      setTimeout(() => setCopiedSha(false), 2000)
    })
  }

  return (
    <Sheet open onOpenChange={(v) => !v && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{image.name}</SheetTitle>
          <SheetDescription>
            <StatusDot state={imageState(image.status)} label={imageStateLabel(image.status)} />
          </SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-4">
          <Section title="Identity">
            <Row label="ID" value={image.id} mono />
            <Row label="Version" value={image.version || "—"} />
            <Row label="OS" value={image.os || "—"} />
            <Row label="Arch" value={image.arch || "—"} />
            <Row label="Format" value={image.format || "—"} />
            <Row label="Firmware" value={image.firmware || "—"} />
          </Section>

          <Section title="Content">
            <Row label="Size" value={formatBytes(image.size_bytes)} />
            <div className="flex items-start justify-between gap-4 text-sm">
              <span className="text-muted-foreground shrink-0">SHA256</span>
              <div className="flex items-center gap-1 min-w-0">
                <span className="font-mono text-xs break-all">{image.checksum || "—"}</span>
                {image.checksum && (
                  <Button variant="ghost" size="icon" className="h-5 w-5 shrink-0" onClick={copySHA}>
                    {copiedSha ? <Check className="h-3 w-3 text-green-500" /> : <Copy className="h-3 w-3" />}
                  </Button>
                )}
              </div>
            </div>
          </Section>

          <Section title="Lifecycle">
            <Row label="Created" value={relativeTime(image.created_at)} />
            <Row label="Finalized" value={relativeTime(image.finalized_at)} />
            {image.build_method && <Row label="Build method" value={image.build_method} />}
            {image.source_url && <Row label="Source URL" value={image.source_url} />}
          </Section>

          {image.tags?.length > 0 && (
            <Section title="Tags">
              <div className="flex flex-wrap gap-1.5">
                {image.tags.map((t) => (
                  <span key={t} className="rounded bg-secondary px-2 py-0.5 text-xs font-mono">{t}</span>
                ))}
              </div>
            </Section>
          )}

          {image.notes && (
            <Section title="Notes">
              <p className="text-sm text-muted-foreground">{image.notes}</p>
            </Section>
          )}

          {image.error_message && (
            <Section title="Error">
              <p className="text-sm text-destructive font-mono text-xs">{image.error_message}</p>
            </Section>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{title}</h3>
      <div className="space-y-1.5">{children}</div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 text-sm">
      <span className="text-muted-foreground shrink-0">{label}</span>
      <span className={cn("text-right break-all", mono && "font-mono text-xs")}>{value ?? "—"}</span>
    </div>
  )
}
