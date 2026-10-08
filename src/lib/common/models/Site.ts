import { z } from 'zod'

export const Site = z.object({
	id: z.string().nonempty(),
	name: z.string().nonempty(),
	description: z.string(),
	host: z.string().nonempty(),
	group: z.string().nonempty(),
	head: z.string(),
	foot: z.string(),
	preview: z.string().or(z.file()).optional(),
	index: z.number().int().nonnegative(),
	// Custom-domain connection state (see internal/domain_provider.go). Optional
	// so older records / non-hosted instances validate without them.
	// domain_dns_records is a JSON column: PocketBase returns it parsed (array),
	// so it's typed loosely like other JSON fields (config, entry value).
	domain_status: z.string().optional(),
	domain_dns_records: z.any().optional(),
	domain_provider_id: z.string().optional(),
	domain_error: z.string().optional(),
	// Where the deployed copy of the site lives when it's hosted outside Primo
	// (see internal/published.go). Validated and normalized server-side.
	public_url: z.string().optional(),
	// Asks search engines not to index the Primo-served copy (X-Robots-Tag,
	// see internal/serve.go), for sites whose live copy is hosted elsewhere.
	noindex: z.boolean().optional(),
	// Go-live webhook state (see internal/deploy.go). The webhook itself is
	// hidden from the record API; deploy_configured only says whether there is
	// one, so editors know to show the Go live button.
	deploy_configured: z.boolean().optional(),
	deployed_at: z.string().optional(),
	deploy_status: z.string().optional()
})

export type Site = z.infer<typeof Site>
