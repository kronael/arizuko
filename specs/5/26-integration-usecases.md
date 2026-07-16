---
status: reference
---

# specs/5/26 — integration use-case corpus (grounding for 5/25)

~120 concrete "agent + service → action" use cases collected 2026-07-16
across four domains, to derive the integration surface (`5/25`). Columns:
service · use case · tool name · read/write · auth method · acquisition
path (mcp = vendor MCP server · openapi = auto-import candidate · rest =
hand `[[ext]]` descriptor · go = Go handler) · scope string. Reference
data; the design + findings live in `5/25`.

## Dev / infra / cloud

| service         | use case                         | tool                          | rw  | auth                            | acquisition   | scope                            |
| --------------- | -------------------------------- | ----------------------------- | --- | ------------------------------- | ------------- | -------------------------------- |
| GitHub          | open a PR                        | `github_pr_open`              | w   | bearer/OAuth                    | mcp           | `ext:github:pr:write`            |
| GitHub          | comment/review a PR              | `github_pr_review_write`      | w   | bearer/OAuth                    | mcp           | `ext:github:pr:write`            |
| GitHub          | list open issues                 | `github_issue_list`           | r   | bearer/OAuth                    | mcp/openapi   | `ext:github:issue:read`          |
| GitHub          | trigger/read a workflow run      | `github_actions_run_trigger`  | w/r | bearer/OAuth                    | mcp           | `ext:github:actions:write`       |
| GitLab          | open/merge an MR                 | `gitlab_mr_open`              | w   | apikey-header/OAuth             | rest          | `ext:gitlab:mr:write`            |
| Gitea           | open PR on self-hosted repo      | `gitea_pr_open`               | w   | bearer/basic                    | openapi       | `ext:gitea:pr:write`             |
| Cloudflare      | upsert a DNS record              | `cloudflare_dns_upsert`       | w   | bearer                          | openapi       | `ext:cloudflare:dns:write`       |
| Cloudflare      | purge cache                      | `cloudflare_cache_purge`      | w   | bearer                          | openapi       | `ext:cloudflare:cache:write`     |
| Porkbun         | upsert DNS record                | `porkbun_dns_upsert`          | w   | json-body                       | rest          | `ext:porkbun:dns:write`          |
| Route53         | upsert a record set              | `route53_record_upsert`       | w   | **SigV4**                       | go            | `ext:aws:route53:write`          |
| Namecheap       | update DNS (XML response)        | `namecheap_dns_upsert`        | w   | apikey-query + IP-allowlist     | rest(xml)     | `ext:namecheap:dns:write`        |
| AWS (S3/EC2)    | put object / describe instances  | `aws_s3_put`                  | w/r | **SigV4**                       | go            | `ext:aws:s3:write`               |
| GCP             | deploy Cloud Run / read logs     | `gcp_run_deploy`              | w   | **OAuth2/SA-JWT**               | go            | `ext:gcp:run:write`              |
| Fly.io          | scale/deploy an app              | `fly_machine_create`          | w/r | bearer                          | openapi       | `ext:fly:machine:write`          |
| Vercel          | trigger a deployment             | `vercel_deploy_trigger`       | w   | bearer                          | openapi       | `ext:vercel:deploy:write`        |
| Vercel          | set project env vars             | `vercel_env_set`              | w   | bearer                          | openapi       | `ext:vercel:env:write`           |
| Netlify         | trigger build / read status      | `netlify_deploy_trigger`      | w/r | bearer/OAuth                    | openapi       | `ext:netlify:deploy:write`       |
| CircleCI        | trigger pipeline / read job      | `circleci_pipeline_trigger`   | w/r | apikey-header                   | openapi       | `ext:circleci:pipeline:write`    |
| Datadog         | query metrics / create monitor   | `datadog_monitor_create`      | r/w | apikey-header (×2)              | openapi       | `ext:datadog:monitor:write`      |
| Grafana         | upsert dashboard / silence alert | `grafana_dashboard_upsert`    | w   | bearer/apikey-header            | openapi       | `ext:grafana:dashboard:write`    |
| Sentry          | resolve an issue                 | `sentry_issue_resolve`        | w/r | bearer                          | openapi       | `ext:sentry:issue:write`         |
| Prometheus      | query metrics                    | `prometheus_query`            | r   | none/basic                      | rest          | `ext:prometheus:metrics:read`    |
| PagerDuty       | trigger/ack/resolve incident     | `pagerduty_incident_trigger`  | w/r | apikey-header (`Token token=`)  | openapi       | `ext:pagerduty:incident:write`   |
| Betterstack     | create incident / update status  | `betterstack_incident_create` | w   | bearer                          | rest          | `ext:betterstack:incident:write` |
| Statuspage      | post an incident update          | `statuspage_incident_update`  | w   | apikey-header (`OAuth <t>`)     | openapi       | `ext:statuspage:incident:write`  |
| Registry        | list/delete an image tag         | `registry_tag_delete`         | w   | bearer-challenge (2-step)       | rest/go       | `ext:registry:tag:write`         |
| Terraform Cloud | trigger a run / read plan        | `tfc_run_trigger`             | w/r | bearer                          | rest(jsonapi) | `ext:tfc:run:write`              |
| Vault           | read/write a secret              | `vault_secret_read`           | r/w | apikey-header (`X-Vault-Token`) | rest          | `ext:vault:secret:read`          |
| Infisical       | fetch a secret                   | `infisical_secret_get`        | r   | bearer (2-step exchange)        | rest/go       | `ext:infisical:secret:read`      |

## Comms / productivity

| service           | use case                        | tool                      | rw  | auth                   | acquisition | scope                         |
| ----------------- | ------------------------------- | ------------------------- | --- | ---------------------- | ----------- | ----------------------------- |
| SendGrid          | send transactional email        | `sendgrid_email_send`     | w   | apikey-header          | openapi     | `ext:sendgrid:email:send`     |
| Postmark          | send email                      | `postmark_email_send`     | w   | apikey-header          | rest        | `ext:postmark:email:send`     |
| Resend            | send templated email            | `resend_email_send`       | w   | bearer                 | openapi     | `ext:resend:email:send`       |
| Mailgun           | send email                      | `mailgun_email_send`      | w   | basic                  | openapi     | `ext:mailgun:email:send`      |
| SMTP/IMAP         | read inbox / send reply         | `mail_read` / `mail_send` | r/w | credentials (non-HTTP) | go          | `ext:mail:inbox:read`         |
| Slack             | post / read / react             | `slack_message_post`      | w/r | bearer (bot token)     | openapi     | `ext:slack:message:write`     |
| Discord           | post a channel message          | `discord_message_post`    | w   | bearer (`Bot <t>`)     | rest        | `ext:discord:message:write`   |
| Telegram (bot)    | send a message                  | `telegram_message_send`   | w   | apikey-query (in path) | rest        | `ext:telegram:message:send`   |
| MS Teams          | post a channel message          | `teams_message_post`      | w   | **OAuth-only** (Graph) | openapi     | `ext:teams:message:write`     |
| Google Calendar   | create event / find free slot   | `gcal_event_create`       | w/r | **OAuth-only**         | openapi     | `ext:gcal:event:write`        |
| Outlook/Graph     | create calendar event           | `outlook_event_create`    | w   | **OAuth-only**         | openapi     | `ext:outlook:event:write`     |
| Cal.com           | book a slot / list slots        | `calcom_booking_create`   | w/r | bearer                 | openapi     | `ext:calcom:booking:write`    |
| Notion            | create page / query database    | `notion_page_write`       | w/r | bearer                 | mcp         | `ext:notion:page:write`       |
| Google Docs/Drive | append to doc / upload file     | `gdocs_append`            | w   | **OAuth-only**         | openapi     | `ext:gdocs:write`             |
| Confluence        | create/update a page            | `confluence_page_write`   | w   | basic/OAuth            | openapi     | `ext:confluence:page:write`   |
| Obsidian          | append to a local note          | `obsidian_note_write`     | w   | apikey-header (plugin) | rest        | `ext:obsidian:note:write`     |
| Linear            | create / update an issue        | `linear_issue_create`     | w   | bearer                 | mcp         | `ext:linear:issue:write`      |
| Jira              | create / transition an issue    | `jira_issue_create`       | w   | basic                  | openapi     | `ext:jira:issue:write`        |
| Asana             | create a task                   | `asana_task_create`       | w   | bearer                 | openapi     | `ext:asana:task:write`        |
| Trello            | create / move a card            | `trello_card_create`      | w   | apikey-query           | rest        | `ext:trello:card:write`       |
| GitHub Issues     | create / search issues          | `github_issue_create`     | w/r | bearer                 | mcp/openapi | `ext:github:issue:write`      |
| Todoist           | create a task                   | `todoist_task_create`     | w   | bearer                 | openapi     | `ext:todoist:task:write`      |
| HubSpot           | create/update a contact or deal | `hubspot_contact_write`   | w   | bearer/OAuth           | openapi     | `ext:hubspot:contact:write`   |
| Salesforce        | create/update a lead            | `salesforce_lead_write`   | w   | **OAuth-only**         | openapi     | `ext:salesforce:lead:write`   |
| Pipedrive         | create a deal / log activity    | `pipedrive_deal_create`   | w   | apikey-query           | openapi     | `ext:pipedrive:deal:write`    |
| Typeform          | read form responses             | `typeform_responses_read` | r   | bearer                 | openapi     | `ext:typeform:responses:read` |
| Calendly          | read scheduled events           | `calendly_event_read`     | r   | bearer                 | openapi     | `ext:calendly:event:read`     |

## Commerce / data / finance

| service       | use case                         | tool                           | rw  | auth                | acquisition | scope                             |
| ------------- | -------------------------------- | ------------------------------ | --- | ------------------- | ----------- | --------------------------------- |
| Stripe        | list charges/customers (triage)  | `stripe_list_charges`          | r   | bearer              | mcp         | `ext:stripe:charges:read`         |
| Stripe        | issue a refund                   | `stripe_refund`                | w   | bearer              | mcp         | `ext:stripe:refund:write`         |
| Stripe        | create a customer                | `stripe_create_customer`       | w   | bearer              | mcp         | `ext:stripe:customer:write`       |
| Stripe        | create a payment link            | `stripe_create_payment_link`   | w   | bearer              | mcp         | `ext:stripe:paymentlink:write`    |
| Stripe Inv.   | draft + finalize/send invoice    | `stripe_finalize_invoice`      | w   | bearer              | mcp         | `ext:stripe:invoice:write`        |
| PayPal        | create order / capture / refund  | `paypal_create_order`          | w   | **OAuth-only**      | openapi     | `ext:paypal:order:write`          |
| Lemon Squeezy | create checkout / cancel sub     | `lemonsqueezy_create_checkout` | w   | apikey-header       | openapi     | `ext:lemonsqueezy:checkout:write` |
| Shopify       | list products / adjust inventory | `shopify_update_inventory`     | w/r | **OAuth-only**      | mcp         | `ext:shopify:inventory:write`     |
| Shopify       | create a manual order            | `shopify_create_order`         | w   | **OAuth-only**      | mcp         | `ext:shopify:order:write`         |
| WooCommerce   | list / update product price      | `woo_update_product_price`     | w/r | basic               | rest        | `ext:woo:product:write`           |
| PostgREST     | query rows / insert / delete     | `postgrest_delete_row`         | r/w | bearer (JWT)        | rest        | `ext:postgrest:rows:write`        |
| Supabase      | query table / run migration      | `supabase_run_migration`       | r/w | apikey-header       | mcp         | `ext:supabase:migration:write`    |
| Firebase      | read/write a Firestore doc       | `firebase_set_document`        | w   | **OAuth-only** (SA) | mcp         | `ext:firebase:doc:write`          |
| Airtable      | list / create records            | `airtable_create_record`       | w   | bearer (PAT)        | openapi     | `ext:airtable:record:write`       |
| Baserow       | list / create rows               | `baserow_create_row`           | w   | apikey-header       | openapi     | `ext:baserow:row:write`           |
| Google Sheets | read range / append row          | `sheets_append_row`            | w   | **OAuth-only**      | openapi     | `ext:gsheets:row:write`           |
| PostHog       | capture event / query insights   | `posthog_query_insights`       | r   | apikey-header       | openapi     | `ext:posthog:insights:read`       |
| Plausible     | pull site stats                  | `plausible_get_stats`          | r   | bearer              | openapi     | `ext:plausible:stats:read`        |
| Mixpanel      | query a saved report             | `mixpanel_query_report`        | r   | basic               | rest        | `ext:mixpanel:report:read`        |
| GA4           | run an ad-hoc report             | `ga4_run_report`               | r   | **OAuth-only**      | rest        | `ext:ga4:report:read`             |
| Brave Search  | web search for grounding         | `brave_web_search`             | r   | apikey-header       | rest/mcp    | `ext:brave:search:read`           |
| Tavily        | agent-oriented web search        | `tavily_search`                | r   | apikey-header       | mcp         | `ext:tavily:search:read`          |
| Pinecone      | upsert/query vectors             | `pinecone_query`               | r   | apikey-header       | mcp         | `ext:pinecone:query:read`         |
| Qdrant        | semantic search                  | `qdrant_search`                | r   | apikey-header       | mcp         | `ext:qdrant:search:read`          |
| QuickBooks    | create/send invoice / record pmt | `quickbooks_create_invoice`    | w   | **OAuth-only**      | openapi     | `ext:quickbooks:invoice:write`    |
| Xero          | create invoice / fetch txns      | `xero_create_invoice`          | w   | **OAuth-only**      | openapi     | `ext:xero:invoice:write`          |
| S3/R2         | upload/fetch object              | `s3_put_object`                | w/r | **SigV4**           | go          | `ext:s3:object:write`             |

## Social / content / AI / misc

(arizuko has native adapters for X/Mastodon/Bluesky/Reddit/LinkedIn — this
bucket is analytics/scheduling/listening + non-social capabilities.)

| service          | use case                           | tool                       | rw  | auth               | acquisition        | scope                           |
| ---------------- | ---------------------------------- | -------------------------- | --- | ------------------ | ------------------ | ------------------------------- |
| Buffer           | schedule a post / pull analytics   | `buffer_schedule_post`     | w/r | bearer (OAuth2)    | rest               | `ext:buffer:schedule:write`     |
| Typefully        | draft/schedule a thread            | `typefully_create_draft`   | w   | apikey-header      | rest               | `ext:typefully:draft:write`     |
| YouTube Data     | video stats / search               | `youtube_get_video_stats`  | r   | apikey-query/OAuth | openapi            | `ext:youtube:stats:read`        |
| WordPress        | publish/update a post              | `wp_create_post`           | w   | basic (app pass)   | rest               | `ext:wordpress:post:write`      |
| Ghost            | publish a post                     | `ghost_create_post`        | w   | json-body (JWT)    | rest               | `ext:ghost:post:write`          |
| Webflow          | create/update CMS item             | `webflow_create_item`      | w   | bearer             | openapi            | `ext:webflow:cms:write`         |
| Contentful       | query entries (CDA)                | `contentful_get_entries`   | r   | bearer             | rest               | `ext:contentful:entry:read`     |
| Sanity           | mutate a document                  | `sanity_mutate`            | w   | bearer             | rest               | `ext:sanity:doc:write`          |
| Figma            | export a frame as PNG/SVG          | `figma_get_image`          | r   | apikey-header      | rest(binary)       | `ext:figma:image:read`          |
| Cloudinary       | upload + transform an image        | `cloudinary_upload`        | w   | apikey (signed)    | rest(binary)       | `ext:cloudinary:asset:write`    |
| Replicate        | run a model prediction (async)     | `replicate_run_prediction` | w   | bearer             | rest(async)        | `ext:replicate:predict:write`   |
| Fal              | run an image/audio job (async)     | `fal_run_job`              | w   | apikey-header      | rest(async)        | `ext:fal:run:write`             |
| OpenAI API       | image / embeddings as a sub-tool   | `openai_generate_image`    | w   | bearer             | openapi            | `ext:openai:image:write`        |
| Anthropic API    | child-model call for a sub-task    | `anthropic_complete`       | w   | apikey-header      | rest               | `ext:anthropic:complete:write`  |
| ElevenLabs       | text-to-speech                     | `elevenlabs_tts`           | w   | apikey-header      | rest(binary)       | `ext:elevenlabs:tts:write`      |
| Whisper/Deepgram | transcribe an audio note           | `whisper_transcribe`       | w   | bearer (multipart) | rest(binary)       | `ext:transcribe:audio:write`    |
| DeepL            | translate a message/document       | `deepl_translate`          | r/w | apikey-header      | rest               | `ext:deepl:translate:write`     |
| Google Maps      | geocode / directions               | `gmaps_directions`         | r   | apikey-query       | openapi            | `ext:gmaps:directions:read`     |
| Mapbox           | static map image                   | `mapbox_static_image`      | r   | apikey-query       | rest(binary)       | `ext:mapbox:staticmap:read`     |
| OpenWeather      | current conditions / forecast      | `openweather_forecast`     | r   | apikey-query       | rest               | `ext:openweather:forecast:read` |
| Firecrawl        | scrape a URL / crawl a site        | `firecrawl_scrape`         | r   | bearer             | mcp/rest           | `ext:firecrawl:scrape:read`     |
| Jina Reader      | fetch a URL as LLM-ready text      | `jina_reader`              | r   | bearer (optional)  | rest               | `ext:jina:reader:read`          |
| ScrapingBee      | render a JS page → HTML/screenshot | `scrapingbee_scrape`       | r   | apikey-query       | rest               | `ext:scrapingbee:scrape:read`   |
| Bitly            | shorten a URL / click analytics    | `bitly_shorten`            | w   | bearer             | openapi            | `ext:bitly:link:write`          |
| Home Assistant   | call a service / read state        | `hass_call_service`        | w/r | bearer (LLT)       | rest               | `ext:hass:service:write`        |
| Exchange rates   | convert currencies                 | `exchangerate_convert`     | r   | apikey-query       | rest               | `ext:fx:convert:read`           |
| PDF generation   | render HTML/URL → PDF              | `pdf_generate`             | w   | apikey-header      | rest(binary/async) | `ext:pdf:render:write`          |
| QR code          | generate a QR image                | `qrcode_generate`          | w   | none/apikey-query  | rest(binary)       | `ext:qrcode:generate:write`     |

## Cross-cutting patterns (feed `5/25`)

- **Auth tiers**: static-cred self-serve (~55–60%) · OAuth-only (~⅓,
  Google/MS/Salesforce/Shopify/PayPal/accounting) · signature (SigV4/JWT →
  Go handler). OAuth is the wall on agent self-serve.
- **Acquisition precedence**: vendor MCP (GitHub/Stripe/Shopify/Supabase/
  Firebase/Notion/Linear/Pinecone/Qdrant/Tavily/Firecrawl) → OpenAPI
  auto-import (the default for API-first) → hand REST descriptor (the
  workhorse for no-spec APIs) → Go handler (SigV4/JWT/2-step/non-HTTP).
- **Stakes ≠ read/write**: money-movement + destructive writes need a
  confirm + idempotency + dry-run, not a bare grant.
- **Descriptor must express**: typed path-params · pagination (cursor/
  offset/jsonapi) · idempotency keys · `response` json/binary/xml · async
  submit+poll pairs · large-text truncation · multi/templated auth headers.
