<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog'
	import { Input } from '$lib/components/ui/input'
	import { Label } from '$lib/components/ui/label'
	import { Plus, X } from 'lucide-svelte'
	import { untrack } from 'svelte'
	import { Sites } from '$lib/pocketbase/collections'
	import { self } from '$lib/pocketbase/managers'
	import type { Site } from '$lib/common/models/Site'

	// How a site is hosted when its published files are deployed outside Primo.
	// Developer-only, like the rest of the site's setup; the server enforces
	// that (internal/published.go, internal/deploy.go), the dashboard only
	// hides the menu item.
	let {
		site,
		open = $bindable(false)
	}: {
		site: Pick<Site, 'id' | 'public_url' | 'noindex'> | null | undefined
		open?: boolean
	} = $props()

	let public_url = $state('')
	let initial_public_url = ''
	let noindex = $state(false)
	let initial_noindex = false
	// Set once the checkbox is clicked, so the public URL stops driving it.
	let noindex_touched = false
	let error = $state('')
	let saving = $state(false)

	// Go-live webhook (internal/deploy.go). Its settings are hidden from the
	// record API, so they're read and written through their own endpoint.
	// Stored header values only come back masked; a header saved with a blank
	// value keeps the value it has.
	type WebhookHeader = { key: number; name: string; value: string; masked: string }
	type WebhookConfig = { url: string; headers: { name: string; value_masked: string }[]; configured: boolean }
	let next_header_key = 0
	let webhook_url = $state('')
	let webhook_headers = $state<WebhookHeader[]>([])
	// null until the config has loaded; the section stays disabled until then.
	let webhook_initial = $state<string | null>(null)
	let webhook_error = $state('')
	// Responses for an earlier opening of the dialog are dropped.
	let webhook_request = 0
	const webhook_snapshot = () => JSON.stringify([webhook_url.trim(), webhook_headers.map((header) => [header.name.trim(), header.value])])
	const webhook_changed = $derived(webhook_initial !== null && webhook_snapshot() !== webhook_initial)

	async function deploy_config_request(site_id: string, method: 'GET' | 'PUT', body?: unknown): Promise<WebhookConfig> {
		const response = await fetch(`${self.instance?.baseURL}/api/primo/deploy-config/${site_id}`, {
			method,
			headers: {
				'Content-Type': 'application/json',
				...(self.instance?.authStore.token ? { Authorization: `Bearer ${self.instance.authStore.token}` } : {})
			},
			body: body === undefined ? undefined : JSON.stringify(body)
		})
		const data = await response.json().catch(() => ({}))
		if (!response.ok) throw new Error(data.message || `Request failed (${response.status})`)
		return data
	}

	function apply_webhook_config(config: WebhookConfig) {
		webhook_url = config.url || ''
		webhook_headers = (config.headers ?? []).map((header) => ({ key: next_header_key++, name: header.name, value: '', masked: header.value_masked }))
		webhook_initial = webhook_snapshot()
	}

	function load_webhook_config(site_id: string) {
		const request = ++webhook_request
		webhook_url = ''
		webhook_headers = []
		webhook_initial = null
		webhook_error = ''
		deploy_config_request(site_id, 'GET')
			.then((config) => request === webhook_request && apply_webhook_config(config))
			.catch((err) => {
				if (request === webhook_request) webhook_error = err instanceof Error ? err.message : String(err)
			})
	}

	// Seed from the record when the dialog opens. The site is read untracked so
	// a realtime update of the record can't overwrite what's being typed.
	$effect(() => {
		if (!open) return
		untrack(() => {
			initial_public_url = site?.public_url || ''
			public_url = initial_public_url
			initial_noindex = !!site?.noindex
			noindex = initial_noindex
			noindex_touched = false
			error = ''
			if (site) load_webhook_config(site.id)
		})
	})

	// Mirrors NormalizePublicURL in internal/published.go, which has the final
	// say; checked here so a typo shows inline instead of failing the save.
	const public_url_error = $derived.by(() => {
		const value = public_url.trim()
		if (!value) return ''
		try {
			const url = new URL(value)
			if ((url.protocol === 'http:' || url.protocol === 'https:') && !/[?#]/.test(value) && !url.username) return ''
		} catch {}
		return 'Enter an absolute http(s) URL without query or fragment, e.g. https://www.example.com'
	})

	// A site with a public URL is live elsewhere, so the Primo copy usually
	// shouldn't compete with it in search results. Suggested, not forced: the
	// box stays editable and isn't touched again once clicked.
	function suggest_noindex(value: string) {
		if (!noindex_touched) noindex = initial_noindex || !!value.trim()
	}

	async function save(event: SubmitEvent) {
		event.preventDefault()
		if (!site || saving || public_url_error) return
		const changes: Partial<Site> = {}
		if (public_url.trim() !== initial_public_url) changes.public_url = public_url.trim()
		if (noindex !== initial_noindex) changes.noindex = noindex
		if (webhook_changed || Object.keys(changes).length) {
			saving = true
			error = ''
			try {
				if (webhook_changed) {
					// Saved first: if the server rejects it, nothing else is saved
					// and the dialog stays open with the error.
					try {
						const config = await deploy_config_request(site.id, 'PUT', {
							url: webhook_url.trim(),
							headers: webhook_headers.filter((header) => header.name.trim()).map((header) => (header.value ? { name: header.name.trim(), value: header.value } : { name: header.name.trim() }))
						})
						apply_webhook_config(config)
						webhook_error = ''
						// Shows or hides the editor's Go live button.
						self.update_record(site.id, { deploy_configured: config.configured })
					} catch (err) {
						webhook_error = err instanceof Error ? err.message : String(err)
						return
					}
				}
				if (Object.keys(changes).length) {
					Sites.update(site.id, changes)
					await self.commit()
				}
			} catch (err) {
				error = err instanceof Error ? err.message : 'Failed to save publishing settings'
				return
			} finally {
				saving = false
			}
		}
		open = false
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="!w-[min(540px,calc(100vw-1rem))] max-w-none max-h-[calc(100vh-2rem)] overflow-y-auto gap-0 pt-11 pb-6 bg-[#1e1e20] border-[#343437] rounded-lg text-[#e4e4e7]">
		<Dialog.Title class="text-[18px] font-medium leading-none tracking-tight text-[#f4f4f5]">Publishing settings</Dialog.Title>
		<p class="text-[13px] leading-[1.65] text-[#a9a9b2]">For deploying this site's published files to another host.</p>
		<form onsubmit={save} class="min-w-0">
			<div class="mt-4 space-y-2">
				<Label for="publishing-public-url">Public URL</Label>
				<Input
					id="publishing-public-url"
					bind:value={public_url}
					oninput={(event) => suggest_noindex(event.currentTarget.value)}
					placeholder="https://www.example.com"
					autocomplete="off"
					spellcheck={false}
				/>
				{#if public_url_error}
					<p class="text-red-500 text-xs">{public_url_error}</p>
				{:else}
					<p class="text-muted-foreground text-xs">Where the deployed copy of this site lives. Used in the downloaded files (sitemap, links) instead of the Primo host.</p>
				{/if}
			</div>
			<div class="mt-4 space-y-1">
				<label class="flex items-center gap-2 text-sm font-medium leading-none">
					<input type="checkbox" bind:checked={noindex} onchange={() => (noindex_touched = true)} class="h-4 w-4 accent-[#ededf0]" />
					Hide the Primo copy from search engines
				</label>
				<p class="text-muted-foreground text-xs pl-6">Use when the live site is hosted elsewhere.</p>
			</div>
			<div class="mt-6 space-y-2 border-t border-[#343437] pt-5">
				<Label for="publishing-webhook-url">Go-live webhook</Label>
				<Input
					id="publishing-webhook-url"
					bind:value={webhook_url}
					placeholder="https://api.github.com/repos/OWNER/REPO/dispatches"
					autocomplete="off"
					spellcheck={false}
					disabled={webhook_initial === null}
				/>
				{#each webhook_headers as header, i (header.key)}
					<div class="flex items-center gap-1.5">
						<Input aria-label="Header name" placeholder="Authorization" bind:value={header.name} class="h-8 basis-[35%] text-xs md:text-xs" autocomplete="off" spellcheck={false} />
						<Input aria-label="Header value" placeholder={header.masked || 'Bearer …'} bind:value={header.value} class="h-8 flex-1 min-w-0 text-xs md:text-xs" autocomplete="off" spellcheck={false} />
						<button type="button" aria-label="Remove header" onclick={() => webhook_headers.splice(i, 1)} class="shrink-0 p-1 text-muted-foreground hover:text-foreground">
							<X class="h-3.5 w-3.5" />
						</button>
					</div>
				{/each}
				{#if webhook_initial !== null}
					<button
						type="button"
						onclick={() => webhook_headers.push({ key: next_header_key++, name: '', value: '', masked: '' })}
						class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
					>
						<Plus class="h-3.5 w-3.5" /> Add header
					</button>
				{/if}
				{#if webhook_error}
					<p class="text-red-500 text-xs">{webhook_error}</p>
				{:else}
					<p class="text-muted-foreground text-xs">Go live POSTs a repository_dispatch-compatible body here, with a one-hour link to the published zip. Leave a saved header value blank to keep it.</p>
				{/if}
			</div>
			{#if error}
				<p class="text-red-500 text-sm mt-3">{error}</p>
			{/if}
			<Dialog.Footer class="mt-6">
				<button type="button" class="pub-btn" onclick={() => (open = false)}>Cancel</button>
				<button type="submit" class="pub-btn primary" disabled={saving || !!public_url_error}>{saving ? 'Saving…' : 'Save'}</button>
			</Dialog.Footer>
		</form>
	</Dialog.Content>
</Dialog.Root>

<style lang="postcss">
	/* Same buttons as ConnectDomain, which opens from the same menu. */
	.pub-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 7px;
		min-height: 36px;
		padding: 8px 13px;
		background: #252528;
		border: 1px solid #3a3a40;
		border-radius: 5px;
		color: #dedee3;
		cursor: pointer;
		text-decoration: none;
		font-size: 12px;
		font-weight: 400;
	}
	.pub-btn:hover {
		background: #303034;
	}
	.pub-btn.primary {
		background: #ededf0;
		color: #202023;
		border-color: #ededf0;
		font-weight: 500;
	}
	.pub-btn.primary:hover {
		background: white;
		border-color: white;
	}
	.pub-btn:disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}
	.pub-btn:focus-visible {
		outline: 2px solid #956e51;
		outline-offset: 3px;
	}
</style>
