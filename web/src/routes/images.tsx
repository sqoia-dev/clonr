import * as React from "react"
import { useNavigate, useSearch } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { formatDistanceToNow } from "date-fns"
import { Search, ChevronUp, ChevronDown, ChevronsUpDown, Copy, Check, Plus, Trash2, AlertTriangle, Upload, Link, Layers, Terminal, FolderOpen, HardDrive } from "lucide-react"
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
import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot } from "@/components/StatusDot"
import { apiFetch } from "@/lib/api"
import type { BaseImage, Bundle, ImageEvent, ListImagesResponse, ListBundlesResponse, ListLocalFilesResponse, LocalFileInfo } from "@/lib/types"
import { cn } from "@/lib/utils"
import { useSSE } from "@/hooks/use-sse"
import { toast } from "@/hooks/use-toast"
import * as tus from "tus-js-client"
import { ImageShell } from "@/components/ImageShell"

interface ImageSearch {
  q?: string
  tab?: string
  sort?: string
  dir?: "asc" | "desc"
  addImage?: string
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
  const [addImageOpen, setAddImageOpen] = React.useState(false)
  const [buildInitramfsOpen, setBuildInitramfsOpen] = React.useState(false)

  // IMG-URL-6: auto-open AddImageSheet from URL param (used by Cmd-K "Add image from URL…").
  React.useEffect(() => {
    if (search.addImage === "1") {
      setAddImageOpen(true)
      navigate({
        to: "/images",
        search: { q: q || undefined, tab: tab === "base" ? undefined : tab, sort: sortCol || undefined, dir: sortDir === "asc" ? undefined : "desc", addImage: undefined },
        replace: true,
      })
    }
  }, [search.addImage]) // eslint-disable-line react-hooks/exhaustive-deps

  function updateSearch(patch: Partial<ImageSearch>) {
    navigate({
      to: "/images",
      search: {
        q: patch.q !== undefined ? patch.q : q || undefined,
        tab: patch.tab !== undefined ? patch.tab : tab === "base" ? undefined : tab,
        sort: patch.sort !== undefined ? patch.sort : sortCol || undefined,
        dir: patch.dir !== undefined ? patch.dir : sortDir === "asc" ? undefined : "desc",
        addImage: undefined,
      },
      replace: true,
    })
  }

  const queryClient = useQueryClient()
  const imageQueryKey = ["images", q, sortCol, sortDir]

  const { data, isLoading: imagesLoading, isError: imagesError } = useQuery<ListImagesResponse>({
    queryKey: imageQueryKey,
    queryFn: () => {
      const params = new URLSearchParams()
      if (q) params.set("search", q)
      if (sortCol) params.set("sort", sortCol)
      if (sortDir) params.set("dir", sortDir)
      return apiFetch<ListImagesResponse>(`/api/v1/images?${params}`)
    },
    // SSE-2: No polling — SSE events trigger targeted invalidation instead.
    staleTime: Infinity,
  })

  // SSE-2: Subscribe to image lifecycle events; invalidate the query on any change.
  useSSE<ImageEvent>({
    path: "/api/v1/images/events",
    onMessage: (event) => {
      if (event.kind === "image.deleted") {
        // Remove deleted image from cache immediately, then refetch list.
        queryClient.setQueryData<ListImagesResponse>(imageQueryKey, (old) => {
          if (!old) return old
          return { ...old, images: old.images.filter((img) => img.id !== event.id) }
        })
      } else if (event.image) {
        // Update or insert the changed image in the cached list.
        queryClient.setQueryData<ListImagesResponse>(imageQueryKey, (old) => {
          if (!old) {
            queryClient.invalidateQueries({ queryKey: imageQueryKey })
            return old
          }
          const exists = old.images.some((img) => img.id === event.id)
          if (exists) {
            return {
              ...old,
              images: old.images.map((img) =>
                img.id === event.id ? (event.image as BaseImage) : img
              ),
            }
          }
          // New image — prepend and bump total.
          return { ...old, images: [event.image as BaseImage, ...old.images], total: old.total + 1 }
        })
      }
    },
  })

  const allImages = data?.images ?? []
  // Base Images: anything that is not an initramfs artifact.
  const baseImages = allImages.filter((img) => img.build_method !== "initramfs")
  // Initramfs: images built by the initramfs build pipeline.
  const initramfsImages = allImages.filter((img) => img.build_method === "initramfs")

  // Bundles — separate endpoint: exposes built-in slurm bundle metadata.
  const { data: bundlesData } = useQuery<ListBundlesResponse>({
    queryKey: ["bundles"],
    queryFn: () => apiFetch<ListBundlesResponse>("/api/v1/bundles"),
    staleTime: Infinity,
  })
  const bundles = bundlesData?.bundles ?? []

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
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={() => setAddImageOpen(true)}>
            <Plus className="h-4 w-4 mr-1" />
            Add Image
          </Button>
          {/* INITRD-3: Build Initramfs button — lives on the Initramfs tab */}
          {tab === "initramfs" && (
            <Button size="sm" variant="outline" onClick={() => setBuildInitramfsOpen(true)}>
              <Layers className="h-4 w-4 mr-1" />
              Build Initramfs
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={() => setAdvanced((a) => !a)}
            className={cn(advanced && "bg-secondary")}
          >
            {advanced ? "Basic view" : "Advanced"}
          </Button>
        </div>
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
              <TabsTrigger value="initramfs">Initramfs ({initramfsImages.length})</TabsTrigger>
            </TabsList>
          </div>

          {/* Base Images tab */}
          <TabsContent value="base" className="flex-1 overflow-auto mt-0">
            {imagesLoading ? (
              <div className="p-4 space-y-2">
                {Array.from({ length: 4 }).map((_, i) => (
                  <div key={i} className="h-10 w-full rounded bg-secondary/40 animate-pulse" />
                ))}
              </div>
            ) : imagesError ? (
              <div className="flex items-center justify-center h-40">
                <p className="text-sm text-destructive">Failed to load images. Reload to retry.</p>
              </div>
            ) : baseImages.length === 0 ? (
              <BaseImagesEmptyState onAddImage={() => setAddImageOpen(true)} />
            ) : (
              <ImageTable
                images={baseImages}
                advanced={advanced}
                onSelect={setSelectedImage}
                handleSort={handleSort}
                SortIcon={SortIcon}
                relativeTime={relativeTime}
              />
            )}
          </TabsContent>

          {/* Bundles tab — read-only, shows built-in slurm bundle from binary */}
          <TabsContent value="bundles" className="flex-1 overflow-auto mt-0">
            {bundles.length === 0 ? (
              <BundlesEmptyState />
            ) : (
              <BundlesTable bundles={bundles} />
            )}
          </TabsContent>

          {/* Initramfs tab — shows built initramfs artifacts */}
          <TabsContent value="initramfs" className="flex-1 overflow-auto mt-0">
            {imagesLoading ? (
              <div className="p-4 space-y-2">
                {Array.from({ length: 4 }).map((_, i) => (
                  <div key={i} className="h-10 w-full rounded bg-secondary/40 animate-pulse" />
                ))}
              </div>
            ) : imagesError ? (
              <div className="flex items-center justify-center h-40">
                <p className="text-sm text-destructive">Failed to load images. Reload to retry.</p>
              </div>
            ) : initramfsImages.length === 0 ? (
              <InitramfsEmptyState onBuild={() => setBuildInitramfsOpen(true)} />
            ) : (
              <ImageTable
                images={initramfsImages}
                advanced={advanced}
                onSelect={setSelectedImage}
                handleSort={handleSort}
                SortIcon={SortIcon}
                relativeTime={relativeTime}
              />
            )}
          </TabsContent>
        </Tabs>
      </div>

      {selectedImage && (
        <ImageSheet image={selectedImage} onClose={() => setSelectedImage(null)} relativeTime={relativeTime} />
      )}
      {/* IMG-URL-4 / IMG-ISO-4: Add Image sheet */}
      <AddImageSheet open={addImageOpen} onClose={() => setAddImageOpen(false)} />
      {/* INITRD-3..5: Build Initramfs sheet */}
      <BuildInitramfsSheet
        open={buildInitramfsOpen}
        onClose={() => setBuildInitramfsOpen(false)}
        images={allImages.filter((img) => img.status === "ready")}
      />
    </div>
  )
}

// ─── AddImageSheet ────────────────────────────────────────────────────────────
// Sprint 4: IMG-URL-4..5 (URL tab) + IMG-ISO-4..7 (Upload tab)

interface AddImageSheetProps {
  open: boolean
  onClose: () => void
}

export function AddImageSheet({ open, onClose }: AddImageSheetProps) {
  const [tab, setTab] = React.useState<"url" | "upload" | "filesystem">("url")

  function handleClose() {
    setTab("url")
    onClose()
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleClose()}>
      <SheetContent side="right" className="w-full sm:max-w-lg overflow-y-auto">
        <SheetHeader>
          <SheetTitle>Add Image</SheetTitle>
          <SheetDescription>Download from a URL, upload an ISO, or use a file already on the server.</SheetDescription>
        </SheetHeader>
        <div className="mt-6">
          <Tabs value={tab} onValueChange={(v) => setTab(v as "url" | "upload" | "filesystem")}>
            <TabsList className="w-full">
              <TabsTrigger value="url" className="flex-1">
                <Link className="h-3.5 w-3.5 mr-1.5" />
                From URL
              </TabsTrigger>
              <TabsTrigger value="upload" className="flex-1">
                <Upload className="h-3.5 w-3.5 mr-1.5" />
                Upload ISO
              </TabsTrigger>
              <TabsTrigger value="filesystem" className="flex-1">
                <FolderOpen className="h-3.5 w-3.5 mr-1.5" />
                Server files
              </TabsTrigger>
            </TabsList>
            <TabsContent value="url" className="mt-4">
              <AddImageFromURL onSuccess={handleClose} />
            </TabsContent>
            <TabsContent value="upload" className="mt-4">
              <AddImageFromISO onSuccess={handleClose} />
            </TabsContent>
            <TabsContent value="filesystem" className="mt-4">
              <AddImageFromFilesystem onSuccess={handleClose} />
            </TabsContent>
          </Tabs>
        </div>
      </SheetContent>
    </Sheet>
  )
}

// ─── AddImageFromURL ──────────────────────────────────────────────────────────
// IMG-URL-4..5

function AddImageFromURL({ onSuccess }: { onSuccess: () => void }) {
  const qc = useQueryClient()
  const [url, setUrl] = React.useState("")
  const [name, setName] = React.useState("")
  const [sha256, setSha256] = React.useState("")
  const [urlError, setUrlError] = React.useState("")
  const [inProgress, setInProgress] = React.useState(false)
  const [progressImageId, setProgressImageId] = React.useState<string | null>(null)
  const [progressStatus, setProgressStatus] = React.useState("")

  // Auto-suggest name from URL.
  React.useEffect(() => {
    if (!url) return
    try {
      const parts = url.split("/")
      const last = parts[parts.length - 1].split("?")[0]
      if (last && !name) setName(last)
    } catch { /* ignore */ }
  }, [url]) // eslint-disable-line react-hooks/exhaustive-deps

  const submitMutation = useMutation({
    mutationFn: () =>
      apiFetch<{ id: string; status: string }>("/api/v1/images/from-url", {
        method: "POST",
        body: JSON.stringify({
          url,
          name: name || undefined,
          expected_sha256: sha256 || undefined,
        }),
      }),
    onSuccess: (res) => {
      setInProgress(true)
      setProgressImageId(res.id)
      setProgressStatus("downloading")
      qc.invalidateQueries({ queryKey: ["images"] })
      toast({ title: "Download started", description: `${name || url} — downloading in background.` })
    },
    onError: (err) => {
      setUrlError(String(err))
    },
  })

  // Watch SSE for progress on the image being downloaded.
  useSSE<ImageEvent>({
    path: "/api/v1/images/events",
    onMessage: (event) => {
      if (!progressImageId || event.id !== progressImageId) return
      if (event.kind === "image.finalized") {
        setProgressStatus("ready")
        qc.invalidateQueries({ queryKey: ["images"] })
        toast({ title: "Download complete", description: name || url })
        onSuccess()
      } else if (event.image?.status === "error") {
        setProgressStatus("error")
        setUrlError(event.image.error_message || "Download failed")
        setInProgress(false)
      }
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setUrlError("")
    if (!url) { setUrlError("URL is required"); return }
    if (!url.startsWith("http://") && !url.startsWith("https://")) {
      setUrlError("URL must use http or https")
      return
    }
    submitMutation.mutate()
  }

  if (inProgress && progressStatus !== "error") {
    return (
      <div className="space-y-4">
        <div className="rounded-md border border-border bg-card p-4 space-y-3">
          <div className="flex items-center gap-2 text-sm">
            <span className="h-2 w-2 rounded-full bg-status-warning animate-pulse shrink-0" />
            <span className="font-medium">{progressStatus === "ready" ? "Download complete" : "Downloading…"}</span>
          </div>
          <p className="text-xs text-muted-foreground font-mono break-all">{url}</p>
          {progressStatus !== "ready" && (
            <div className="h-1.5 rounded-full bg-secondary overflow-hidden">
              <div className="h-full bg-status-warning animate-pulse" style={{ width: "60%" }} />
            </div>
          )}
        </div>
        <Button variant="ghost" size="sm" onClick={onSuccess} className="w-full">
          Close (download continues in background)
        </Button>
      </div>
    )
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <AddImageField label="URL *">
        <Input
          placeholder="https://example.com/image.iso"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          className={cn(urlError && "border-destructive")}
        />
        {urlError && <p className="text-xs text-destructive mt-1">{urlError}</p>}
      </AddImageField>
      <AddImageField label="Name (auto-filled from URL)">
        <Input
          placeholder="my-image"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </AddImageField>
      <AddImageField
        label="Expected SHA256 (optional)"
        hint="If provided, download fails if the computed hash doesn't match."
      >
        <Input
          placeholder="sha256hex…"
          value={sha256}
          onChange={(e) => setSha256(e.target.value)}
          className="font-mono text-xs"
        />
      </AddImageField>
      <div className="flex gap-2 pt-2">
        <Button type="submit" className="flex-1" disabled={submitMutation.isPending}>
          {submitMutation.isPending ? "Starting…" : "Download"}
        </Button>
        <Button type="button" variant="ghost" onClick={onSuccess}>Cancel</Button>
      </div>
    </form>
  )
}

// ─── AddImageFromISO ──────────────────────────────────────────────────────────
// IMG-ISO-4..7: TUS resumable upload

const TUS_UPLOAD_ENDPOINT = "/api/v1/uploads/"
const ISO_WARN_BYTES = 10 * 1024 * 1024 * 1024 // 10 GB
const HASH_MAX_BYTES = 2 * 1024 * 1024 * 1024 // 2 GB

function AddImageFromISO({ onSuccess }: { onSuccess: () => void }) {
  const qc = useQueryClient()
  const [file, setFile] = React.useState<File | null>(null)
  const [name, setName] = React.useState("")
  const [uploadProgress, setUploadProgress] = React.useState(0)
  const [uploading, setUploading] = React.useState(false)
  const [paused, setPaused] = React.useState(false)
  const [error, setError] = React.useState("")
  const [clientHash, setClientHash] = React.useState("")
  const [hashProgress, setHashProgress] = React.useState(0)
  const uploadRef = React.useRef<tus.Upload | null>(null)
  const inputRef = React.useRef<HTMLInputElement>(null)

  function handleFileChange(f: File) {
    setFile(f)
    setError("")
    setClientHash("")
    setHashProgress(0)
    if (!name) setName(f.name)
    // IMG-ISO-6: compute SHA256 client-side for <2GB files.
    if (f.size < HASH_MAX_BYTES) {
      computeFileSHA256(f, setHashProgress).then(setClientHash).catch(() => {/* skip */})
    }
  }

  async function computeFileSHA256(f: File, onProgress: (p: number) => void): Promise<string> {
    const buf = await f.slice(0, f.size).arrayBuffer()
    onProgress(50)
    const hashBuf = await crypto.subtle.digest("SHA-256", buf)
    onProgress(100)
    return Array.from(new Uint8Array(hashBuf)).map((b) => b.toString(16).padStart(2, "0")).join("")
  }

  function startUpload() {
    if (!file) return
    setError("")
    setUploading(true)
    setPaused(false)

    const upload = new tus.Upload(file, {
      endpoint: TUS_UPLOAD_ENDPOINT,
      retryDelays: [0, 1000, 3000, 5000],
      metadata: {
        filename: file.name,
        filetype: file.type || "application/octet-stream",
        name: name || file.name,
      },
      chunkSize: 10 * 1024 * 1024, // 10 MiB chunks
      onProgress(bytesSent, bytesTotal) {
        setUploadProgress(Math.round((bytesSent / bytesTotal) * 100))
      },
      onSuccess() {
        const uploadId = upload.url?.split("/").pop() ?? ""
        // Register the upload as an image.
        apiFetch<{ id: string }>("/api/v1/images/from-upload", {
          method: "POST",
          body: JSON.stringify({
            upload_id: uploadId,
            name: name || file.name,
            expected_sha256: clientHash || undefined,
          }),
        }).then((res) => {
          qc.invalidateQueries({ queryKey: ["images"] })
          toast({ title: "Upload complete", description: `${name || file.name} registered as image ${res.id.slice(0, 8)}` })
          onSuccess()
        }).catch((err) => {
          setError(String(err))
          setUploading(false)
        })
      },
      onError(err) {
        setError(String(err))
        setUploading(false)
        setPaused(false)
      },
    })
    uploadRef.current = upload
    upload.start()
  }

  function pauseUpload() {
    uploadRef.current?.abort()
    setPaused(true)
  }

  function resumeUpload() {
    if (!file) return
    setPaused(false)
    uploadRef.current?.start()
  }

  return (
    <div className="space-y-4">
      {/* IMG-ISO-7: warn on large files */}
      {file && file.size > ISO_WARN_BYTES && (
        <div className="flex items-start gap-2 rounded-md border border-status-warning/40 bg-status-warning/5 p-3 text-xs text-status-warning">
          <AlertTriangle className="h-4 w-4 shrink-0 mt-0.5" />
          <span>Large ISO (&gt;10 GB) — consider hosting it internally and using From URL instead.</span>
        </div>
      )}

      {/* Drop zone */}
      <div
        className={cn(
          "rounded-md border-2 border-dashed border-border p-8 text-center cursor-pointer hover:border-primary/50 transition-colors",
          file && "border-primary/30 bg-primary/5"
        )}
        onClick={() => inputRef.current?.click()}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault()
          const f = e.dataTransfer.files[0]
          if (f) handleFileChange(f)
        }}
        role="button"
        tabIndex={0}
        aria-label="Click or drag ISO file to upload"
        onKeyDown={(e) => e.key === "Enter" && inputRef.current?.click()}
      >
        <input
          ref={inputRef}
          type="file"
          className="hidden"
          accept=".iso,.img,.tar,.tar.gz,.tar.bz2,.tar.xz"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) handleFileChange(f) }}
        />
        {file ? (
          <div className="space-y-1">
            <p className="text-sm font-medium">{file.name}</p>
            <p className="text-xs text-muted-foreground">{formatBytes(file.size)}</p>
            {hashProgress > 0 && hashProgress < 100 && (
              <p className="text-xs text-muted-foreground">Computing SHA256… {hashProgress}%</p>
            )}
            {clientHash && (
              <p className="text-xs text-muted-foreground font-mono">{clientHash.slice(0, 16)}…</p>
            )}
          </div>
        ) : (
          <div className="space-y-1">
            <Upload className="h-8 w-8 mx-auto text-muted-foreground" />
            <p className="text-sm text-muted-foreground">Drag and drop or click to select</p>
            <p className="text-xs text-muted-foreground">.iso, .img, .tar, .tar.gz, .tar.xz</p>
          </div>
        )}
      </div>

      {file && (
        <AddImageField label="Image name">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={file.name} />
        </AddImageField>
      )}

      {error && <p className="text-xs text-destructive">{error}</p>}

      {uploading && (
        <div className="space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>{paused ? "Paused" : "Uploading…"}</span>
            <span>{uploadProgress}%</span>
          </div>
          <div className="h-1.5 rounded-full bg-secondary overflow-hidden">
            <div
              className={cn("h-full bg-primary transition-all", paused && "opacity-50")}
              style={{ width: `${uploadProgress}%` }}
            />
          </div>
        </div>
      )}

      {file && (
        <div className="flex gap-2 pt-2">
          {!uploading || paused ? (
            <Button
              className="flex-1"
              onClick={paused ? resumeUpload : startUpload}
              disabled={!file}
            >
              {paused ? "Resume" : "Upload"}
            </Button>
          ) : (
            <Button variant="outline" className="flex-1" onClick={pauseUpload}>
              Pause
            </Button>
          )}
          <Button variant="ghost" onClick={onSuccess}>Cancel</Button>
        </div>
      )}
    </div>
  )
}

function AddImageField({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-sm text-muted-foreground">{label}</label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}

// ─── AddImageFromFilesystem ───────────────────────────────────────────────────
// ISO-FS-3..4: list and register files already on the server import dir.

function AddImageFromFilesystem({ onSuccess }: { onSuccess: () => void }) {
  const qc = useQueryClient()
  const [selectedFile, setSelectedFile] = React.useState<LocalFileInfo | null>(null)
  const [name, setName] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")

  const { data, isLoading, isError, refetch } = useQuery<ListLocalFilesResponse>({
    queryKey: ["images-local-files"],
    queryFn: () => apiFetch<ListLocalFilesResponse>("/api/v1/images/local-files"),
    staleTime: 15000,
  })

  const files = data?.files ?? []
  const importDir = data?.import_dir ?? "/var/lib/clustr/iso"

  function handleSelectFile(f: LocalFileInfo) {
    setSelectedFile(f)
    setName((prev) => prev || f.name.replace(/\.(iso|img|qcow2|raw)$/i, ""))
    setError("")
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!selectedFile) { setError("Select a file first"); return }
    if (!name.trim()) { setError("Name is required"); return }
    setSubmitting(true)
    setError("")
    try {
      await apiFetch<{ id: string }>("/api/v1/images/from-local-file", {
        method: "POST",
        body: JSON.stringify({ path: selectedFile.path, name: name.trim() }),
      })
      qc.invalidateQueries({ queryKey: ["images"] })
      toast({ title: "Image registered", description: `${name} added from server filesystem.` })
      onSuccess()
    } catch (err) {
      setError(String(err))
    } finally {
      setSubmitting(false)
    }
  }

  if (isLoading) {
    return (
      <div className="space-y-2 py-4">
        {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-12 w-full" />)}
      </div>
    )
  }

  if (isError) {
    return (
      <div className="py-4 text-center space-y-2">
        <p className="text-sm text-destructive">Failed to list server files</p>
        <Button size="sm" variant="outline" onClick={() => refetch()}>Retry</Button>
      </div>
    )
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="rounded-md border border-border bg-secondary/30 px-3 py-2 text-xs text-muted-foreground">
        <HardDrive className="h-3.5 w-3.5 inline mr-1.5 align-text-bottom" />
        Files in <code className="font-mono">{importDir}</code> — drop ISOs here to make them appear.
      </div>

      {files.length === 0 ? (
        <div className="py-6 text-center space-y-1">
          <p className="text-sm text-muted-foreground">No .iso, .img, .qcow2 or .raw files found</p>
          <p className="text-xs text-muted-foreground">Copy files to <code className="font-mono">{importDir}</code> on the server.</p>
          <Button size="sm" variant="outline" className="mt-2" onClick={() => refetch()}>Refresh</Button>
        </div>
      ) : (
        <div className="space-y-1 max-h-52 overflow-y-auto rounded-md border border-border">
          {files.map((f) => (
            <button
              key={f.path}
              type="button"
              className={cn(
                "w-full flex items-center gap-3 px-3 py-2.5 text-left hover:bg-secondary/40 transition-colors",
                selectedFile?.path === f.path && "bg-secondary"
              )}
              onClick={() => handleSelectFile(f)}
            >
              <FolderOpen className="h-4 w-4 text-muted-foreground shrink-0" />
              <div className="flex-1 min-w-0">
                <p className="text-sm font-mono truncate">{f.name}</p>
                <p className="text-xs text-muted-foreground">{formatBytes(f.size)} · {new Date(f.mtime).toLocaleDateString()}</p>
              </div>
              {selectedFile?.path === f.path && (
                <Check className="h-4 w-4 text-primary shrink-0" />
              )}
            </button>
          ))}
        </div>
      )}

      {selectedFile && (
        <AddImageField label="Image name *">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="rocky9-base"
          />
        </AddImageField>
      )}

      {error && <p className="text-xs text-destructive">{error}</p>}

      <div className="flex gap-2 pt-2">
        <Button type="submit" className="flex-1" disabled={!selectedFile || submitting}>
          {submitting ? "Registering…" : "Register Image"}
        </Button>
        <Button type="button" variant="ghost" onClick={onSuccess}>Cancel</Button>
      </div>
    </form>
  )
}

// ─── ImageTable ───────────────────────────────────────────────────────────────
// Shared table component for both Base Images and Initramfs tabs.

interface ImageTableProps {
  images: BaseImage[]
  advanced: boolean
  onSelect: (img: BaseImage) => void
  handleSort: (col: string) => void
  SortIcon: (props: { col: string }) => React.ReactElement
  relativeTime: (iso?: string) => string
}

function ImageTable({ images, advanced, onSelect, handleSort, SortIcon, relativeTime }: ImageTableProps) {
  return (
    <Table>
      <caption className="sr-only">Cluster images</caption>
      <TableHeader>
        <TableRow>
          <TableHead scope="col">
            <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("name")}>
              Name <SortIcon col="name" />
            </button>
          </TableHead>
          <TableHead scope="col">Status</TableHead>
          <TableHead scope="col">
            <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("version")}>
              Version <SortIcon col="version" />
            </button>
          </TableHead>
          <TableHead scope="col">Size</TableHead>
          <TableHead scope="col">SHA256</TableHead>
          {advanced && <TableHead scope="col">OS / Arch</TableHead>}
          <TableHead scope="col">
            <button className="flex items-center gap-1 hover:text-foreground" onClick={() => handleSort("created_at")}>
              Created <SortIcon col="created_at" />
            </button>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {images.map((img) => (
          <TableRow key={img.id} className="cursor-pointer" onClick={() => onSelect(img)}>
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
  )
}

// ─── BundlesTable ─────────────────────────────────────────────────────────────
// Read-only table showing built-in bundle metadata from GET /api/v1/bundles.

function BundlesTable({ bundles }: { bundles: Bundle[] }) {
  return (
    <Table>
      <caption className="sr-only">Built-in software bundles</caption>
      <TableHeader>
        <TableRow>
          <TableHead scope="col">Name</TableHead>
          <TableHead scope="col">Slurm Version</TableHead>
          <TableHead scope="col">Bundle Version</TableHead>
          <TableHead scope="col">SHA256</TableHead>
          <TableHead scope="col">Kind</TableHead>
          <TableHead scope="col">Source</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {bundles.map((b) => (
          <TableRow key={b.name}>
            <TableCell>
              <span className="font-medium text-sm">{b.name}</span>
            </TableCell>
            <TableCell className="font-mono text-xs text-muted-foreground">{b.slurm_version}</TableCell>
            <TableCell className="font-mono text-xs text-muted-foreground">{b.bundle_version}</TableCell>
            <TableCell className="font-mono text-xs text-muted-foreground">
              {b.sha256 ? b.sha256.slice(0, 12) + "…" : "—"}
            </TableCell>
            <TableCell className="text-xs text-muted-foreground">{b.kind}</TableCell>
            <TableCell className="text-xs text-muted-foreground">{b.source}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

// ─── Empty states ─────────────────────────────────────────────────────────────

function BaseImagesEmptyState({ onAddImage }: { onAddImage?: () => void }) {
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
      {onAddImage && (
        <Button onClick={onAddImage} size="sm">
          <Plus className="h-4 w-4 mr-1" />
          Add Image
        </Button>
      )}
    </div>
  )
}

function BundlesEmptyState() {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-2 p-8 text-center">
      <h2 className="text-base font-semibold">No additional bundles installed</h2>
      <p className="text-sm text-muted-foreground">
        The built-in slurm bundle ships with clustr-serverd.
      </p>
    </div>
  )
}

function InitramfsEmptyState({ onBuild }: { onBuild: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-64 gap-4 p-8 text-center">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">No initramfs built yet</h2>
        <p className="text-sm text-muted-foreground">
          Click &apos;Build Initramfs&apos; above to create one from a base image and bundle.
        </p>
      </div>
      <Button size="sm" variant="outline" onClick={onBuild}>
        <Layers className="h-4 w-4 mr-1" />
        Build Initramfs
      </Button>
    </div>
  )
}

function ImageSheet({ image, onClose, relativeTime }: { image: BaseImage; onClose: () => void; relativeTime: (iso?: string) => string }) {
  const qc = useQueryClient()
  const [copiedSha, setCopiedSha] = React.useState(false)
  const [deleteExpanded, setDeleteExpanded] = React.useState(false)
  const [deleteConfirm, setDeleteConfirm] = React.useState("")
  const [deleteError, setDeleteError] = React.useState("")
  const [shellOpen, setShellOpen] = React.useState(false)

  function copySHA() {
    navigator.clipboard.writeText(image.checksum).then(() => {
      setCopiedSha(true)
      setTimeout(() => setCopiedSha(false), 2000)
    })
  }

  // IMG-DEL-2/4: optimistic delete with rollback on 409.
  const deleteMutation = useMutation({
    mutationFn: () =>
      apiFetch<void>(`/api/v1/images/${image.id}`, { method: "DELETE" }),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ["images"] })
      const prev = qc.getQueryData<ListImagesResponse>(["images"])
      // Optimistic remove.
      qc.setQueryData<ListImagesResponse>(["images"], (old) => {
        if (!old) return old
        return { ...old, images: old.images.filter((img) => img.id !== image.id) }
      })
      return { prev }
    },
    onSuccess: () => {
      toast({ title: "Image deleted", description: image.name })
      onClose()
    },
    onError: (err, _v, ctx) => {
      // Rollback.
      if (ctx?.prev) qc.setQueryData(["images"], ctx.prev)
      const msg = String(err)
      if (msg.includes("image_in_use") || msg.includes("in use") || msg.includes("409")) {
        setDeleteError("Cannot delete: image is assigned to one or more nodes. Reimage them first.")
      } else {
        setDeleteError(msg)
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["images"] })
    },
  })

  function confirmDelete() {
    if (deleteConfirm !== image.name) return
    setDeleteError("")
    deleteMutation.mutate()
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

          {/* SHELL-4..6: xterm.js shell drawer — renders outside the sheet stack to cover full viewport */}
          {shellOpen && <ImageShell image={image} onClose={() => setShellOpen(false)} />}

          {/* SHELL-4: shell button — opens xterm.js full-screen drawer (admin-only on server) */}
          {image.status === "ready" && (
            <div className="pt-4 border-t border-border">
              <Button
                variant="outline"
                size="sm"
                className="w-full"
                onClick={() => setShellOpen(true)}
              >
                <Terminal className="h-3.5 w-3.5 mr-1.5" />
                Open shell
              </Button>
            </div>
          )}

          {/* IMG-DEL-2: inline destructive delete with typed name confirm */}
          <div className="pt-4 border-t border-border space-y-3">
            {!deleteExpanded ? (
              <Button
                variant="outline"
                size="sm"
                className="text-destructive border-destructive/40 hover:bg-destructive/10 w-full"
                onClick={() => { setDeleteExpanded(true); setDeleteError("") }}
              >
                <Trash2 className="h-3.5 w-3.5 mr-1.5" />
                Delete image
              </Button>
            ) : (
              <div className="rounded-md border border-destructive/30 bg-destructive/5 p-4 space-y-3">
                <div className="flex items-center gap-2 text-sm font-medium text-destructive">
                  <AlertTriangle className="h-4 w-4 shrink-0" />
                  Delete image — this cannot be undone
                </div>
                {deleteError && (
                  <p className="text-xs text-destructive">{deleteError}</p>
                )}
                <div className="space-y-1">
                  <p className="text-xs text-muted-foreground">
                    Type <code className="font-mono">{image.name}</code> to confirm:
                  </p>
                  <Input
                    className="font-mono text-xs"
                    placeholder={image.name}
                    value={deleteConfirm}
                    onChange={(e) => setDeleteConfirm(e.target.value)}
                  />
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="destructive"
                    size="sm"
                    className="flex-1"
                    disabled={deleteConfirm !== image.name || deleteMutation.isPending}
                    onClick={confirmDelete}
                  >
                    {deleteMutation.isPending ? "Deleting…" : "Confirm delete"}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => { setDeleteExpanded(false); setDeleteConfirm(""); setDeleteError("") }}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}

// ─── BuildInitramfsSheet ──────────────────────────────────────────────────────
// INITRD-3..7: build initramfs from a base image with live SSE log streaming.

interface BuildInitramfsSheetProps {
  open: boolean
  onClose: () => void
  images: BaseImage[]
}

function BuildInitramfsSheet({ open, onClose, images }: BuildInitramfsSheetProps) {
  const qc = useQueryClient()
  const [baseImageId, setBaseImageId] = React.useState("")
  const [imgName, setImgName] = React.useState("")
  const [running, setRunning] = React.useState(false)
  const [logLines, setLogLines] = React.useState<string[]>([])
  const [doneImageId, setDoneImageId] = React.useState<string | null>(null)
  const [errorMsg, setErrorMsg] = React.useState("")
  const abortRef = React.useRef<AbortController | null>(null)
  const logEndRef = React.useRef<HTMLDivElement | null>(null)

  // Auto-scroll log panel (INITRD-4).
  React.useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logLines])

  function handleClose() {
    if (running && abortRef.current) {
      abortRef.current.abort()
    }
    setRunning(false)
    setLogLines([])
    setDoneImageId(null)
    setErrorMsg("")
    setBaseImageId("")
    setImgName("")
    onClose()
  }

  async function handleBuild() {
    setRunning(true)
    setLogLines([])
    setDoneImageId(null)
    setErrorMsg("")

    const ctrl = new AbortController()
    abortRef.current = ctrl

    try {
      // INITRD-6: abort controller doubles as cancel signal.
      const res = await fetch("/api/v1/initramfs/build", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({
          base_image_id: baseImageId || undefined,
          name: imgName || undefined,
        }),
        signal: ctrl.signal,
      })
      if (!res.ok) {
        const txt = await res.text().catch(() => "unknown error")
        setErrorMsg(`Server error ${res.status}: ${txt}`)
        setRunning(false)
        return
      }

      const reader = res.body?.getReader()
      if (!reader) {
        setErrorMsg("No response body from server")
        setRunning(false)
        return
      }

      const decoder = new TextDecoder()
      let buf = ""

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split("\n")
        buf = lines.pop() ?? ""
        for (const line of lines) {
          if (!line.startsWith("data: ")) continue
          try {
            const evt = JSON.parse(line.slice(6))
            if (evt.type === "log") {
              setLogLines((prev) => [...prev, evt.line])
            } else if (evt.type === "done") {
              setDoneImageId(evt.image_id)
              setRunning(false)
              qc.invalidateQueries({ queryKey: ["images"] })
              toast({ title: "Initramfs built", description: `Image ${evt.image_id?.slice(0, 8)} is ready.` })
            } else if (evt.type === "error") {
              setErrorMsg(evt.message ?? "Build failed")
              setRunning(false)
            }
          } catch {
            // non-JSON SSE line (keep-alives etc)
          }
        }
      }
    } catch (err: unknown) {
      if ((err as Error)?.name !== "AbortError") {
        setErrorMsg(String(err))
      }
      setRunning(false)
    }
  }

  async function handleCancel() {
    if (abortRef.current) abortRef.current.abort()
    // Also signal server-side cancel.
    try {
      await fetch("/api/v1/initramfs/builds/current", {
        method: "DELETE",
        credentials: "include",
      })
    } catch {
      // Best-effort
    }
    setRunning(false)
    setLogLines((prev) => [...prev, "(build cancelled by operator)"])
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && handleClose()}>
      <SheetContent side="right" className="w-full sm:max-w-lg overflow-y-auto">
        <SheetHeader>
          <SheetTitle>Build Initramfs</SheetTitle>
          <SheetDescription>
            Build the PXE initramfs and register it as an image. The existing system initramfs will also be updated.
          </SheetDescription>
        </SheetHeader>
        <div className="mt-6 space-y-4">
          {!running && !doneImageId && (
            <>
              <div className="space-y-1">
                <label className="text-sm text-muted-foreground">Base Image (optional)</label>
                <select
                  className="w-full text-sm border border-border bg-background rounded-md px-3 py-1.5"
                  value={baseImageId}
                  onChange={(e) => setBaseImageId(e.target.value)}
                >
                  <option value="">Current system kernel (default)</option>
                  {images.map((img) => (
                    <option key={img.id} value={img.id}>
                      {img.name} {img.version} ({img.id.slice(0, 8)})
                    </option>
                  ))}
                </select>
                <p className="text-xs text-muted-foreground">
                  Selects which kernel modules to bundle. Leave blank to use the running kernel.
                </p>
              </div>
              <div className="space-y-1">
                <label className="text-sm text-muted-foreground">Name (optional)</label>
                <Input
                  placeholder="initramfs-custom"
                  value={imgName}
                  onChange={(e) => setImgName(e.target.value)}
                  className="text-sm"
                />
              </div>
              <Button className="w-full" onClick={handleBuild}>
                <Layers className="h-4 w-4 mr-2" />
                Start Build
              </Button>
            </>
          )}

          {/* Live log panel (INITRD-4) */}
          {(running || logLines.length > 0) && (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Build log</h3>
                {running && (
                  <Button variant="ghost" size="sm" className="h-6 px-2 text-xs text-destructive" onClick={handleCancel}>
                    Cancel
                  </Button>
                )}
              </div>
              <div className="rounded-md border border-border bg-card font-mono text-xs p-3 h-64 overflow-y-auto space-y-0.5">
                {logLines.map((line, i) => (
                  <div key={i} className="text-muted-foreground leading-relaxed">{line}</div>
                ))}
                {running && (
                  <div className="flex items-center gap-1.5 text-status-warning">
                    <span className="h-1.5 w-1.5 rounded-full bg-status-warning animate-pulse shrink-0" />
                    Building…
                  </div>
                )}
                <div ref={logEndRef} />
              </div>
            </div>
          )}

          {/* Error state */}
          {errorMsg && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive flex items-start gap-2">
              <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
              {errorMsg}
            </div>
          )}

          {/* INITRD-5: success with "View image" link */}
          {doneImageId && (
            <div className="rounded-md border border-status-healthy/30 bg-status-healthy/5 p-3 text-sm space-y-2">
              <p className="font-medium text-status-healthy">Build complete</p>
              <p className="text-xs text-muted-foreground font-mono">{doneImageId}</p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  handleClose()
                }}
              >
                Close
              </Button>
            </div>
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
