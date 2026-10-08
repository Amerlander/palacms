<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog'
	import { Input } from '$lib/components/ui/input'
	import { Label } from '$lib/components/ui/label'
	import { untrack } from 'svelte'
	import { Sites } from '$lib/pocketbase/collections'
	import { self } from '$lib/pocketbase/managers'
	import type { Site } from '$lib/common/models/Site'

	// How a site is hosted when its published files are deployed outside Primo.
	// Developer-only, like the rest of the site's setup; the server enforces
	// that (internal/published.go), the dashboard only hides the menu item.
	let {
		site,
		open = $bindable(false)
	}: {
		site: Pick<Site, 'id' | 'public_url'> | null | undefined
		open?: boolean
	} = $props()

	let public_url = $state('')
	let initial_public_url = ''
	let error = $state('')
	let saving = $state(false)

	// Seed from the record when the dialog opens. The site is read untracked so
	// a realtime update of the record can't overwrite what's being typed.
	$effect(() => {
		if (!open) return
		untrack(() => {
			initial_public_url = site?.public_url || ''
			public_url = initial_public_url
			error = ''
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

	async function save(event: SubmitEvent) {
		event.preventDefault()
		if (!site || saving || public_url_error) return
		const changes: Partial<Site> = {}
		if (public_url.trim() !== initial_public_url) changes.public_url = public_url.trim()
		if (Object.keys(changes).length) {
			saving = true
			error = ''
			try {
				Sites.update(site.id, changes)
				await self.commit()
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
				<Input id="publishing-public-url" bind:value={public_url} placeholder="https://www.example.com" autocomplete="off" spellcheck={false} />
				{#if public_url_error}
					<p class="text-red-500 text-xs">{public_url_error}</p>
				{:else}
					<p class="text-muted-foreground text-xs">Where the deployed copy of this site lives. Used in the downloaded files (sitemap, links) instead of the Primo host.</p>
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
