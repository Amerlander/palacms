<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog'
	import Icon from '@iconify/svelte'
	import { PaneGroup, Pane, PaneResizer } from 'paneforge'
	import Fields, { setFieldEntries } from '$lib/builder/components/Fields/FieldsContent.svelte'
	import Content from '$lib/builder/components/Content.svelte'
	import * as _ from 'lodash-es'
	import CodeEditor from '$lib/builder/components/CodeEditor/CodeMirror.svelte'
	import TextInput from '$lib/builder/ui/TextInput.svelte'
	import { site_context, hide_dynamic_field_types_context } from '$lib/builder/stores/context'
	import { Sites, SiteFields, SiteEntries } from '$lib/pocketbase/collections'
	import { current_user } from '$lib/pocketbase/user'
	import { browser } from '$app/environment'
	import { useContent } from '$lib/Content.svelte'
	import { locale } from '$lib/builder/stores/app/misc.js'
	import { self } from '$lib/pocketbase/managers'
	import { beforeNavigate } from '$app/navigation'
	import { read_only } from '$lib/pocketbase/author_mode'

	let { onClose, has_unsaved_changes = $bindable(false) } = $props()

	const { value: site } = site_context.get()
	const fields = $derived(site?.fields() ?? [])
	const entries = $derived(site?.entries() ?? [])
	const site_data = $derived(useContent(site, { target: 'cms' })?.[$locale] ?? {})

	hide_dynamic_field_types_context.set(true)

	const initial_code = { head: site?.head, foot: site?.foot, public_url: site?.public_url || '' }
	const initial_data = _.cloneDeep(site_data)

	let head = $state(site?.head || '')
	let foot = $state(site?.foot || '')
	let public_url = $state(site?.public_url || '')

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

	let disableSave = $state(false)

	beforeNavigate((nav) => {
		if (has_unsaved_changes) {
			// Prevent navigation when there are unsaved changes
			nav.cancel()
			alert('You have unsaved changes. Please save before navigating away.')
		}
	})

	// Compare current state to initial data
	$effect(() => {
		const code_changed = head !== initial_code.head || foot !== initial_code.foot || public_url !== initial_code.public_url
		const data_changed = !_.isEqual(initial_data, site_data)
		has_unsaved_changes = code_changed || data_changed
	})

	// Add beforeunload listener to warn about unsaved changes
	$effect(() => {
		if (!browser) return

		const handleBeforeUnload = (e) => {
			if (has_unsaved_changes) {
				e.preventDefault()
				e.returnValue = ''
				return ''
			}
		}

		window.addEventListener('beforeunload', handleBeforeUnload)
		return () => window.removeEventListener('beforeunload', handleBeforeUnload)
	})

	async function saveComponent() {
		// Browse mode hides the Save button; any other path here has to match.
		if ($read_only) return
		if (!site) {
			return
		}
		if (public_url_error) return

		disableSave = true
		try {
			Sites.update(site.id, {
				head,
				foot,
				// Only developers see the field; the server rejects public_url
				// changes from anyone else, so don't send it unless it changed.
				...(public_url !== initial_code.public_url ? { public_url: public_url.trim() } : {})
			})

			await self.commit()

			console.log('Site saved successfully')
			if (onClose) onClose()
		} catch (error) {
			console.error('Error saving site:', error)
			throw error
		} finally {
			disableSave = false
		}
	}
</script>

<Dialog.Header
	title="Site"
	icon="gg:website"
	button={$read_only
		? undefined
		: {
				label: 'Save',
				onclick: saveComponent,
				disabled: disableSave || !!public_url_error
			}}
/>

{#if site}
	<main class="SiteEditor">
		{#if $current_user?.siteRole === 'developer'}
			<PaneGroup direction="horizontal" style="display: flex;" autoSaveId="SiteEditor-horizontal">
				<Pane defaultSize={50}>
					<Fields
						entity={site}
						{fields}
						{entries}
						create_field={async (data) => {
							// Get the highest index for fields at this level
							const siblingFields = (fields ?? []).filter((f) => (data?.parent ? f.parent === data.parent : !f.parent))
							const nextIndex = Math.max(...siblingFields.map((f) => f.index || 0), -1) + 1

							SiteFields.create({
								type: 'text',
								key: '',
								label: '',
								config: null,
								site: site.id,
								...data,
								index: nextIndex
							})
						}}
						oninput={(values) => {
							setFieldEntries({
								fields,
								entries,
								updateEntry: SiteEntries.update,
								createEntry: SiteEntries.create,
								values
							})
						}}
						onchange={({ id, data }) => {
							SiteFields.update(id, data)
						}}
						ondelete={(field) => {
							SiteFields.delete(field.id)
						}}
						ondelete_entry={(entry_id) => {
							SiteEntries.delete(entry_id)
						}}
					/>
				</Pane>
				<PaneResizer class="PaneResizer-primary">
					<div class="icon primary">
						<Icon icon="mdi:drag-vertical-variant" />
					</div>
				</PaneResizer>
				<Pane defaultSize={50}>
					<PaneGroup direction="vertical" autoSaveId="SiteEditor-vertical">
						<Pane minSize={1.4}>
							<div class="container" style="margin-bottom: 1rem">
								<div class="public-url">
									<TextInput label="Public URL" bind:value={public_url} placeholder="https://www.example.com" disabled={$read_only} />
									{#if public_url_error}
										<p class="public-url-error">{public_url_error}</p>
									{:else}
										<p class="public-url-help">Where the deployed copy of this site lives. Used in the downloaded files (sitemap, links) instead of the Primo host.</p>
									{/if}
								</div>
								<span class="primo--field-label">Head HTML</span>
								<CodeEditor mode="html" bind:value={head} disabled={$read_only} on:save={saveComponent} />
							</div>
						</Pane>
						<PaneResizer class="PaneResizer-secondary">
							<div class="icon secondary">
								<Icon icon="mdi:drag-horizontal-variant" />
							</div>
						</PaneResizer>
						<Pane minSize={1.4}>
							<div class="container">
								<span class="primo--field-label">Body Footer HTML</span>
								<CodeEditor mode="html" bind:value={foot} disabled={$read_only} on:save={saveComponent} />
							</div>
						</Pane>
					</PaneGroup>
				</Pane>
			</PaneGroup>
		{:else}
			<Content
				entity={site}
				{fields}
				{entries}
				oninput={(values) => {
					setFieldEntries({
						fields,
						entries,
						updateEntry: SiteEntries.update,
						createEntry: SiteEntries.create,
						values
					})
				}}
				ondelete={(entry_id) => {
					SiteEntries.delete(entry_id)
				}}
			/>
		{/if}
	</main>
{/if}

<style lang="postcss">
	.SiteEditor {
	}
	main {
		display: flex; /* to help w/ positioning child items in code view */
		background: var(--primo-color-black);
		color: var(--color-gray-2);
		padding: 0.5rem;
		flex: 1;
		overflow: hidden;

		--Button-bg: var(--color-gray-8);
		--Button-bg-hover: var(--color-gray-9);
	}

	:global(.PaneResizer-primary) {
		height: 100%;
		width: 3px;
		background: var(--color-gray-9);
		display: grid;
		place-content: center;
	}
	:global(.PaneResizer-secondary) {
		width: 100%;
		display: grid;
		place-content: center;
		justify-content: center;
		height: 2px;
		background: var(--color-gray-8);
		margin-block: 0.5rem;
	}
	.icon {
		position: relative;
		background: var(--color-gray-8);
		border-radius: 2px;
		color: var(--color-gray-3);

		&.primary {
			padding-block: 3px;
		}

		&.secondary {
			padding-inline: 3px;
		}
	}
	.container {
		padding-left: 0.75rem;
		display: flex;
		flex-direction: column;
		max-height: 100%;
	}
	.public-url {
		margin-bottom: 1rem;
	}
	.public-url-help,
	.public-url-error {
		margin-top: 0.375rem;
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-gray-4);
	}
	.public-url-error {
		color: #f87171;
	}
</style>
