package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// Both secrets are readable from the environment because argv is world-readable
// in a ps listing and is written to shell history.
const (
	envVaultKey          = "SCRAPFLY_VAULT_KEY"
	envVaultServiceToken = "SCRAPFLY_VAULT_SERVICE_TOKEN"
)

var (
	errMissingVaultKey = fmt.Errorf("missing vault key: set %s or pass --vault-key", envVaultKey)
	errMissingToken    = fmt.Errorf("missing service-account token: set %s or pass --token", envVaultServiceToken)
)

// vaultLinkedService is the linked_service discriminator. The API keeps a registry
// of these and answers 400 for anything absent from it. Collapses into the
// go-scrapfly type once that module publishes one.
type vaultLinkedService string

const vaultLinkedServiceOnePassword vaultLinkedService = "1password"

// vaultLinkedServices mirrors the API's linkedServiceRegistry, which stays the
// authority: this copy only exists to catch a typo before the request.
var vaultLinkedServices = []vaultLinkedService{vaultLinkedServiceOnePassword}

func vaultLinkedServiceChoices() string {
	names := make([]string, len(vaultLinkedServices))
	for i, s := range vaultLinkedServices {
		names[i] = string(s)
	}
	return strings.Join(names, "|")
}

// parseVaultLinkedService rejects an unregistered name before the request is built.
// The server would answer 400 anyway; naming the accepted values is the point.
func parseVaultLinkedService(raw string) (vaultLinkedService, error) {
	for _, s := range vaultLinkedServices {
		if vaultLinkedService(raw) == s {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown --service %q (accepted: %s)", raw, vaultLinkedServiceChoices())
}

func newVaultCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage Cloud Browser credential vaults, their items and their linked service",
		Long: `Manage Cloud Browser credential vaults.

A vault holds per-origin credentials (password, passkey, cookie, TOTP seed,
opaque blob) that the Cloud Browser injects over CDP on the session's first
page. Attach one to a session with "scrapfly browser --vault <name>
--vault-key <key>".

Every vault is encrypted under a key generated on create and returned once.
Scrapfly keeps no copy: a lost key means the items cannot be read, and no
response ever contains a plaintext item secret. Pass the key with --vault-key
or in ` + envVaultKey + `.

Vault names are alphanumeric, since the name is what "browser --vault" takes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newVaultListCmd(flags),
		newVaultCreateCmd(flags),
		newVaultGetCmd(flags),
		newVaultUpdateCmd(flags),
		newVaultDeleteCmd(flags),
		newVaultRotateCmd(flags),
		newVaultItemCmd(flags),
		newVaultServiceCmd(flags),
	)
	return cmd
}

func newVaultListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vaults in the key's project and environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultList()
			return emitVault(flags, "vault.list", res, err, func(w io.Writer, res map[string]any) {
				vaults := vaultRecords(res, "vaults")
				if len(vaults) == 0 {
					out.Pretty(w, "no vaults")
					return
				}
				for _, v := range vaults {
					prettyVaultRecord(w, v)
				}
			})
		},
	}
}

func newVaultCreateCmd(flags *rootFlags) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:     "create <name>",
		Short:   "Create a vault and print its one-time key",
		Long:    "Create a vault. The response carries the vault key once; store it before the process exits.",
		Example: `  scrapfly vault create checkout --description "storefront logins"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultCreate(args[0], description)
			return emitVault(flags, "vault.create", res, err, func(w io.Writer, res map[string]any) {
				prettyVaultKey(w, res)
				prettyVaultField(w, res, "vault")
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "free-form description")
	return cmd
}

func newVaultGetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <vault-id>",
		Short: "Show one vault, including its link and last sync",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultGet(args[0])
			return emitVault(flags, "vault.get", res, err, func(w io.Writer, res map[string]any) {
				prettyVaultField(w, res, "vault")
			})
		},
	}
}

func newVaultUpdateCmd(flags *rootFlags) *cobra.Command {
	var name, description string
	cmd := &cobra.Command{
		Use:   "update <vault-id>",
		Short: "Rename a vault or replace its description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// An empty field means "keep" server-side, so a call with neither
			// flag set is a no-op the operator would read as a successful edit.
			if name == "" && description == "" {
				return fmt.Errorf("--name or --description is required")
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultUpdate(args[0], name, description)
			return emitVault(flags, "vault.update", res, err, nil)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new alphanumeric name")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	return cmd
}

func newVaultDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <vault-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a vault and every item in it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultDelete(args[0])
			return emitVault(flags, "vault.delete", res, err, nil)
		},
	}
}

func newVaultRotateCmd(flags *rootFlags) *cobra.Command {
	var vaultKey string
	cmd := &cobra.Command{
		Use:   "rotate <vault-id>",
		Short: "Rewrap every item under a fresh key and print the new one",
		Long: `Rewrap every item under a fresh key.

Takes the CURRENT key and returns the new one once. The old key reads nothing
in this vault afterwards, so any session or script still holding it has to be
updated in the same pass.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := resolveVaultKey(vaultKey)
			if err != nil {
				return err
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultRotate(args[0], key)
			return emitVault(flags, "vault.rotate", res, err, func(w io.Writer, res map[string]any) {
				if !prettyVaultKey(w, res) {
					out.Pretty(w, "%s", vaultMessage(res))
				}
			})
		},
	}
	bindVaultKeyFlag(cmd, &vaultKey, "current vault key")
	return cmd
}

// ----------------------------------------------------------------------
// Items
// ----------------------------------------------------------------------

type vaultItemFlags struct {
	itemType     string
	label        string
	origin       string
	username     string
	secretFile   string
	secretInline string
	metadata     string
	vaultKey     string
}

func (f *vaultItemFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.itemType, "type", "", "password | passkey | cookie | totp | blob")
	cmd.Flags().StringVar(&f.label, "label", "", "operator-facing label")
	cmd.Flags().StringVar(&f.origin, "origin", "", "origin the credential is injected on")
	cmd.Flags().StringVar(&f.username, "username", "", "username carried alongside the secret")
	cmd.Flags().StringVar(&f.secretFile, "secret", "", "path to a JSON file with the secret object (use - for stdin)")
	cmd.Flags().StringVar(&f.secretInline, "secret-inline", "", "inline JSON secret object; prefer --secret, inline values land in shell history")
	cmd.Flags().StringVar(&f.metadata, "metadata", "", "inline JSON metadata object")
	bindVaultKeyFlag(cmd, &f.vaultKey, "vault key")
}

// buildVaultItemBody assembles the wire body. The secret object is the flat
// per-type shape the API documents: {"password":"…"}, {"name":…,"domain":…},
// {"seed":…}, {"credentialId":…,"privateKey":…}, {"data":…}.
func buildVaultItemBody(f *vaultItemFlags) (map[string]any, bool, error) {
	secret, err := loadSecretPayload(f.secretFile, f.secretInline)
	if err != nil {
		return nil, false, err
	}
	body := map[string]any{}
	if f.itemType != "" {
		body["type"] = f.itemType
	}
	if f.label != "" {
		body["label"] = f.label
	}
	if f.origin != "" {
		body["origin"] = f.origin
	}
	if f.username != "" {
		body["username"] = f.username
	}
	if f.metadata != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(f.metadata), &metadata); err != nil {
			return nil, false, fmt.Errorf("--metadata is not valid JSON: %w", err)
		}
		body["metadata"] = metadata
	}
	if secret != nil {
		body["secret"] = secret
	}
	return body, secret != nil, nil
}

func newVaultItemCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Manage the credentials held in a vault",
		Long: `Manage the credentials held in a vault.

"item list" returns metadata and the sealed blob, never a plaintext secret:
the key stays on this side and the API cannot decrypt an item.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newVaultItemListCmd(flags),
		newVaultItemCreateCmd(flags),
		newVaultItemUpdateCmd(flags),
		newVaultItemDeleteCmd(flags),
	)
	return cmd
}

func newVaultItemListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list <vault-id>",
		Short: "List items with their provenance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultItemList(args[0])
			return emitVault(flags, "vault.item.list", res, err, func(w io.Writer, res map[string]any) {
				items := vaultRecords(res, "items")
				if len(items) == 0 {
					out.Pretty(w, "no items")
					return
				}
				for _, it := range items {
					source := vaultString(it, "source")
					if source == "" {
						source = "manual"
					}
					out.Pretty(w, "id=%s type=%s label=%s origin=%s source=%s",
						vaultString(it, "id"), vaultString(it, "type"),
						vaultString(it, "label"), vaultString(it, "origin"), source)
				}
			})
		},
	}
}

func newVaultItemCreateCmd(flags *rootFlags) *cobra.Command {
	var f vaultItemFlags
	cmd := &cobra.Command{
		Use:   "create <vault-id>",
		Short: "Seal a new credential into a vault",
		Example: `  scrapfly vault item create 01J8… --type password \
    --label "storefront" --origin https://web-scraping.dev/login \
    --username user123 --secret ./password.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.itemType == "" {
				return fmt.Errorf("--type is required")
			}
			body, hasSecret, err := buildVaultItemBody(&f)
			if err != nil {
				return err
			}
			if !hasSecret {
				return fmt.Errorf("--secret <file> or --secret-inline <json> is required")
			}
			key, err := resolveVaultKey(f.vaultKey)
			if err != nil {
				return err
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultItemCreate(args[0], key, body)
			return emitVault(flags, "vault.item.create", res, err, func(w io.Writer, res map[string]any) {
				it, ok := res["item"].(map[string]any)
				if !ok {
					out.Pretty(w, "%s", vaultMessage(res))
					return
				}
				out.Pretty(w, "id=%s type=%s label=%s",
					vaultString(it, "id"), vaultString(it, "type"), vaultString(it, "label"))
			})
		},
	}
	f.bind(cmd)
	return cmd
}

func newVaultItemUpdateCmd(flags *rootFlags) *cobra.Command {
	var f vaultItemFlags
	cmd := &cobra.Command{
		Use:   "update <vault-id> <item-id>",
		Short: "Patch an item, optionally rotating its secret",
		Long: `Patch an item. Non-empty fields overwrite the stored row.

The vault key is needed only when the patch carries a secret, and --type is
needed with it: the secret object is parsed against the item type. Rows owned
by a linked service are refused, the next sync would revert the edit.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, rotating, err := buildVaultItemBody(&f)
			if err != nil {
				return err
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to patch: pass --label, --origin, --username, --metadata or a secret")
			}
			var key string
			if rotating {
				if f.itemType == "" {
					return fmt.Errorf("--type is required when the patch carries a secret")
				}
				if key, err = resolveVaultKey(f.vaultKey); err != nil {
					return err
				}
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultItemUpdate(args[0], args[1], key, body)
			return emitVault(flags, "vault.item.update", res, err, nil)
		},
	}
	f.bind(cmd)
	return cmd
}

func newVaultItemDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <vault-id> <item-id>",
		Aliases: []string{"rm"},
		Short:   "Delete one item",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserVaultItemDelete(args[0], args[1])
			return emitVault(flags, "vault.item.delete", res, err, nil)
		},
	}
}

// ----------------------------------------------------------------------
// Linked service
// ----------------------------------------------------------------------

type vaultServiceFlags struct {
	service           string
	token             string
	upstreamVaultID   string
	upstreamVaultName string
	titleFilter       string
	tags              []string
	syncMode          string
	syncTTL           time.Duration
	vaultKey          string
}

// bindRequiredService binds the discriminator for POST /service, which has no
// server-side default and rejects an empty one.
func (f *vaultServiceFlags) bindRequiredService(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.service, "service", string(vaultLinkedServiceOnePassword),
		"linked service to mirror from: "+vaultLinkedServiceChoices())
}

// bindOptionalService binds it for the probe, which the server defaults to
// 1password itself. Left empty the field is omitted rather than guessed here.
func (f *vaultServiceFlags) bindOptionalService(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.service, "service", "",
		"linked service to probe: "+vaultLinkedServiceChoices()+"; omitted lets the server default it")
}

func (f *vaultServiceFlags) bindSelection(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.token, "token", "", "provider service-account token; prefer "+envVaultServiceToken)
	cmd.Flags().StringVar(&f.upstreamVaultID, "upstream-vault-id", "", "provider vault id to mirror")
	cmd.Flags().StringVar(&f.upstreamVaultName, "upstream-vault-name", "", "provider vault title to mirror, when the id is unknown")
	cmd.Flags().StringVar(&f.titleFilter, "title-filter", "", "mirror only items whose title matches")
	cmd.Flags().StringArrayVar(&f.tags, "tag", nil, "mirror only items carrying this tag (repeatable)")
	cmd.Flags().StringVar(&f.syncMode, "sync-mode", "", "manual | on_session (default on_session)")
	cmd.Flags().DurationVar(&f.syncTTL, "sync-ttl", 0, "minimum age before an on_session sync refetches (default 15m)")
	bindVaultKeyFlag(cmd, &f.vaultKey, "vault key")
}

// linkedServiceData is the non-secret selection document. token_item_id is
// omitted on purpose: the server binds the sealed token row itself and the
// stored id wins over anything a body carries.
func (f *vaultServiceFlags) linkedServiceData() map[string]any {
	data := map[string]any{}
	if f.upstreamVaultID != "" {
		data["vault_id"] = f.upstreamVaultID
	}
	if f.upstreamVaultName != "" {
		data["vault_name"] = f.upstreamVaultName
	}
	if f.titleFilter != "" {
		data["title_filter"] = f.titleFilter
	}
	if len(f.tags) > 0 {
		data["tags"] = f.tags
	}
	if f.syncMode != "" {
		data["sync_mode"] = f.syncMode
	}
	if f.syncTTL > 0 {
		data["sync_ttl_s"] = int(f.syncTTL.Seconds())
	}
	return data
}

func newVaultServiceCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Link a vault to an external secret manager and drive its sync",
		Long: `Link a vault to an external secret manager.

The provider's service-account token is sealed under the vault key, so linking
and rotating a token both need the key. Mirrored items belong to the syncer:
editing or deleting one is refused, the next pass would revert it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newVaultServiceLinkCmd(flags),
		newVaultServiceUpdateCmd(flags),
		newVaultServiceUnlinkCmd(flags),
		newVaultServiceSyncCmd(flags),
		newVaultServiceTestCmd(flags),
	)
	return cmd
}

func newVaultServiceLinkCmd(flags *rootFlags) *cobra.Command {
	var f vaultServiceFlags
	cmd := &cobra.Command{
		Use:   "link <vault-id>",
		Short: "Link a vault to a secret manager",
		Example: `  SCRAPFLY_VAULT_SERVICE_TOKEN=… SCRAPFLY_VAULT_KEY=… \
    scrapfly vault service link 01J8… --upstream-vault-id abcd1234 --sync-mode on_session`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := resolveServiceToken(f.token)
			if err != nil {
				return err
			}
			// Mandatory here: the token is sealed under this key, and a
			// well-formed wrong one would link the vault and leave a token every
			// later sync cannot open.
			key, err := resolveVaultKey(f.vaultKey)
			if err != nil {
				return err
			}
			service, err := parseVaultLinkedService(f.service)
			if err != nil {
				return err
			}
			res, err := vaultServiceCall(cmd.Context(), flags, http.MethodPost,
				vaultServicePath(args[0], ""), nil, key, map[string]any{
					"linked_service":      string(service),
					"token":               token,
					"linked_service_data": f.linkedServiceData(),
				})
			return emitVault(flags, "vault.service.link", res, err, func(w io.Writer, res map[string]any) {
				prettyVaultField(w, res, "vault")
			})
		},
	}
	f.bindRequiredService(cmd)
	f.bindSelection(cmd)
	return cmd
}

func newVaultServiceUpdateCmd(flags *rootFlags) *cobra.Command {
	var f vaultServiceFlags
	cmd := &cobra.Command{
		Use:   "update <vault-id>",
		Short: "Rotate the token or replace the selection rules",
		Long: `Rotate the token or replace the selection rules.

The selection document is replaced, not merged: pass every rule you want kept.
The vault key is required only when --token is set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// No linked_service: PATCH re-decodes against the vault's stored
			// discriminator and ignores the body field, so a link can never
			// switch provider.
			body := map[string]any{}
			data := f.linkedServiceData()
			if len(data) > 0 {
				body["linked_service_data"] = data
			}
			var key string
			token := resolveOptionalServiceToken(f.token)
			if token != "" {
				body["token"] = token
				var err error
				if key, err = resolveVaultKey(f.vaultKey); err != nil {
					return err
				}
			}
			res, err := vaultServiceCall(cmd.Context(), flags, http.MethodPatch,
				vaultServicePath(args[0], ""), nil, key, body)
			return emitVault(flags, "vault.service.update", res, err, func(w io.Writer, res map[string]any) {
				prettyVaultField(w, res, "vault")
			})
		},
	}
	f.bindSelection(cmd)
	return cmd
}

func newVaultServiceUnlinkCmd(flags *rootFlags) *cobra.Command {
	var keepItems, dropItems bool
	cmd := &cobra.Command{
		Use:   "unlink <vault-id>",
		Short: "Remove the link, keeping or dropping the mirrored items",
		Long: `Remove the link.

Exactly one of --keep-items and --drop-items is required. Dropping is
irreversible and the items only exist upstream afterwards, so the operator asks
for it rather than inheriting it from a default. The sealed provider token is
deleted either way.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keepItems == dropItems {
				return fmt.Errorf("exactly one of --keep-items and --drop-items is required")
			}
			res, err := vaultServiceCall(cmd.Context(), flags, http.MethodDelete,
				vaultServicePath(args[0], ""), url.Values{"keep_items": {strconv.FormatBool(keepItems)}},
				"", nil)
			return emitVault(flags, "vault.service.unlink", res, err, nil)
		},
	}
	cmd.Flags().BoolVar(&keepItems, "keep-items", false, "leave the mirrored items in the vault as unmanaged rows")
	cmd.Flags().BoolVar(&dropItems, "drop-items", false, "delete every mirrored item")
	return cmd
}

func newVaultServiceSyncCmd(flags *rootFlags) *cobra.Command {
	var vaultKey string
	cmd := &cobra.Command{
		Use:   "sync <vault-id>",
		Short: "Force a sync now, bypassing the TTL and the failure back-off",
		Long: `Force a sync now.

Bypasses both the TTL and the hour-long back-off a failed run leaves behind.
The server budget is 25s; a provider outage leaves the existing mirror
untouched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := resolveVaultKey(vaultKey)
			if err != nil {
				return err
			}
			res, err := vaultServiceCall(cmd.Context(), flags, http.MethodPost,
				vaultServicePath(args[0], "/sync"), nil, key, nil)
			return emitVault(flags, "vault.service.sync", res, err, func(w io.Writer, res map[string]any) {
				out.Pretty(w, "status=%s imported=%d updated=%d deleted=%d skipped=%d unmirrored=%d",
					vaultString(res, "status"), vaultNumber(res, "imported"), vaultNumber(res, "updated"),
					vaultNumber(res, "deleted"), vaultNumber(res, "skipped"), vaultNumber(res, "unmirrored"))
				for _, warning := range vaultStrings(res, "warnings") {
					out.Pretty(w, "warning: %s", warning)
				}
			})
		},
	}
	bindVaultKeyFlag(cmd, &vaultKey, "vault key")
	return cmd
}

func newVaultServiceTestCmd(flags *rootFlags) *cobra.Command {
	var f vaultServiceFlags
	cmd := &cobra.Command{
		Use:   "test <vault-id>",
		Short: "Probe the token and list the upstream vaults it can see",
		Long: `Probe the token and list the upstream vaults it can see.

With no token supplied the probe uses the token already sealed in the vault,
which is how a live link is checked. With a token it answers for a vault that is
not linked yet, so the upstream vault id is known before linking. The server
budget is 10s.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The key is required even for an unsaved token: the endpoint reads
			// the vault to answer, so it verifies the key first.
			key, err := resolveVaultKey(f.vaultKey)
			if err != nil {
				return err
			}
			// A nil body is what selects the sealed-token probe: the handler
			// reads the request body only when it is non-empty.
			var body map[string]any
			if token := resolveOptionalServiceToken(f.token); token != "" {
				body = map[string]any{"token": token}
				// Sent only when asked for: the endpoint defaults an absent
				// discriminator to 1password on its own.
				if f.service != "" {
					service, serr := parseVaultLinkedService(f.service)
					if serr != nil {
						return serr
					}
					body["linked_service"] = string(service)
				}
			}
			res, err := vaultServiceCall(cmd.Context(), flags, http.MethodPost,
				vaultServicePath(args[0], "/test"), nil, key, body)
			return emitVault(flags, "vault.service.test", res, err, func(w io.Writer, res map[string]any) {
				out.Pretty(w, "item_count=%d", vaultNumber(res, "item_count"))
				for _, v := range vaultRecords(res, "vaults_visible") {
					out.Pretty(w, "upstream id=%s title=%s", vaultString(v, "id"), vaultString(v, "title"))
				}
				for _, warning := range vaultStrings(res, "warnings") {
					out.Pretty(w, "warning: %s", warning)
				}
			})
		},
	}
	f.bindOptionalService(cmd)
	f.bindSelection(cmd)
	return cmd
}

// ----------------------------------------------------------------------
// Secret input
// ----------------------------------------------------------------------

func bindVaultKeyFlag(cmd *cobra.Command, target *string, what string) {
	cmd.Flags().StringVar(target, "vault-key", "",
		fmt.Sprintf("base64 %s (overrides %s)", what, envVaultKey))
}

func resolveVaultKey(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if key := os.Getenv(envVaultKey); key != "" {
		return key, nil
	}
	return "", errMissingVaultKey
}

func resolveServiceToken(flagValue string) (string, error) {
	if token := resolveOptionalServiceToken(flagValue); token != "" {
		return token, nil
	}
	return "", errMissingToken
}

func resolveOptionalServiceToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(envVaultServiceToken)
}

// loadSecretPayload reads the secret object from a file, stdin or the inline
// flag. A file or stdin keeps the plaintext out of argv.
func loadSecretPayload(file, inline string) (map[string]any, error) {
	if file == "" && inline == "" {
		return nil, nil
	}
	return loadConfigPayload(file, inline)
}

// ----------------------------------------------------------------------
// Linked-service transport
// ----------------------------------------------------------------------

func vaultServicePath(vaultID, suffix string) string {
	return "/vault/" + url.PathEscape(vaultID) + "/service" + suffix
}

// vaultServiceCall issues one linked-service request. These five endpoints are
// absent from the go-scrapfly release go.mod pins, so they are built here; drop
// this once the SDK ships them.
//
// The vault key rides in X-Vault-Key, never in the query string: a query string
// is copied into access logs and error reports, a header is redacted by
// convention.
func vaultServiceCall(ctx context.Context, flags *rootFlags, method, path string, query url.Values, vaultKey string, body map[string]any) (map[string]any, error) {
	client, err := buildClient(flags)
	if err != nil {
		return nil, err
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("key", client.APIKey())
	reqURL := resolveBrowserRESTHost(flags) + path + "?" + query.Encode()

	// A nil map means no body at all: the probe endpoint reads the request only
	// when it is non-empty, and an explicit "null" would not be one.
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "scrapfly-cli/vault")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if vaultKey != "" {
		req.Header.Set("X-Vault-Key", vaultKey)
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if flags.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	resp, err := (&http.Client{Timeout: flags.timeout, Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, vaultAPIError(resp.StatusCode, raw)
	}
	res := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("vault service response is not JSON (http %d)", resp.StatusCode)
		}
	}
	return res, nil
}

// resolveBrowserRESTHost mirrors the SDK's own normalisation: callers configure
// the CDP wss:// entry point, the REST endpoints live on the same host under the
// matching http scheme.
func resolveBrowserRESTHost(flags *rootFlags) string {
	host := flags.browserHost
	if host == "" {
		host = os.Getenv("SCRAPFLY_BROWSER_HOST")
	}
	if host == "" {
		host = "https://browser.scrapfly.io"
	}
	switch {
	case strings.HasPrefix(host, "wss://"):
		host = "https://" + strings.TrimPrefix(host, "wss://")
	case strings.HasPrefix(host, "ws://"):
		host = "http://" + strings.TrimPrefix(host, "ws://")
	}
	return strings.TrimRight(host, "/")
}

// vaultAPIError keeps the typed ERR::BROWSER::VAULT_PROVIDER_* codes in the JSON
// error envelope so a caller can tell "rotate the token" from "retry later".
func vaultAPIError(status int, body []byte) error {
	var doc struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(body, &doc); err == nil {
		message := doc.Message
		if message == "" {
			message = doc.Reason
		}
		if message != "" || doc.Code != "" {
			return &out.APIError{Code: doc.Code, Message: message, HTTPStatus: status}
		}
	}
	return fmt.Errorf("vault service request failed (http %d)", status)
}

// ----------------------------------------------------------------------
// Output
// ----------------------------------------------------------------------

// emitVault is the single exit for every vault command. It owns the error path
// because a *url.Error stringifies the request URL, which carries the api key.
func emitVault(flags *rootFlags, product string, res map[string]any, err error, pretty func(io.Writer, map[string]any)) error {
	if err != nil {
		return redactURLError(err)
	}
	if flags.pretty {
		if pretty != nil {
			pretty(os.Stdout, res)
			return nil
		}
		out.Pretty(os.Stdout, "%s", vaultMessage(res))
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, product, res)
}

// redactURLError rewrites the URL a transport error carries through
// wsURLSecretParams, the CLI's redaction list.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s %s: %w", urlErr.Op, redactWSURL(urlErr.URL), urlErr.Err)
	}
	return err
}

// prettyVaultKey is the only place the CLI prints a vault key. Create and rotate
// return it once and the server keeps no copy, so pretty mode has to print it:
// WriteSuccess emits nothing when --pretty is set.
func prettyVaultKey(w io.Writer, res map[string]any) bool {
	key := vaultString(res, "key")
	if key == "" {
		return false
	}
	out.Pretty(w, "vault_key=%s", key)
	out.Pretty(w, "Save it now. Scrapfly stores no copy and will not show it again.")
	return true
}

// prettyVaultField falls back to the server message so pretty mode never prints
// nothing on a call that succeeded.
func prettyVaultField(w io.Writer, res map[string]any, field string) {
	record, ok := res[field].(map[string]any)
	if !ok {
		out.Pretty(w, "%s", vaultMessage(res))
		return
	}
	prettyVaultRecord(w, record)
}

func prettyVaultRecord(w io.Writer, v map[string]any) {
	line := fmt.Sprintf("id=%s name=%s items=%d type=%s",
		vaultString(v, "id"), vaultString(v, "name"),
		vaultNumber(v, "item_count"), vaultString(v, "vault_type"))
	if service := vaultString(v, "linked_service"); service != "" {
		line += fmt.Sprintf(" service=%s sync=%s", service, vaultString(v, "linked_sync_status"))
	}
	out.Pretty(w, "%s", line)
}

func vaultMessage(res map[string]any) string {
	if message := vaultString(res, "message"); message != "" {
		return message
	}
	return "ok"
}

func vaultString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// vaultNumber reads a count off an untyped response: encoding/json decodes every
// number into float64.
func vaultNumber(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

func vaultStrings(m map[string]any, key string) []string {
	raw, _ := m[key].([]any)
	values := make([]string, 0, len(raw))
	for _, entry := range raw {
		if s, ok := entry.(string); ok {
			values = append(values, s)
		}
	}
	return values
}

func vaultRecords(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	records := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if record, ok := entry.(map[string]any); ok {
			records = append(records, record)
		}
	}
	return records
}
