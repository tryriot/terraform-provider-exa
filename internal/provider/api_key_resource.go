package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tryriot/terraform-provider-exa/internal/exa"
)

var (
	_ resource.Resource                     = &apiKeyResource{}
	_ resource.ResourceWithConfigure        = &apiKeyResource{}
	_ resource.ResourceWithImportState      = &apiKeyResource{}
	_ resource.ResourceWithConfigValidators = &apiKeyResource{}
	_ resource.ResourceWithModifyPlan       = &apiKeyResource{}
)

func NewAPIKeyResource() resource.Resource {
	return &apiKeyResource{}
}

type apiKeyResource struct {
	client *exa.Client
}

type apiKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	RateLimit   types.Int64  `tfsdk:"rate_limit"`
	BudgetCents types.Int64  `tfsdk:"budget_cents"`
	Secret      types.String `tfsdk:"secret"`
	TeamID      types.String `tfsdk:"team_id"`
	CreatedAt   types.String `tfsdk:"created_at"`

	Keepers                    types.Map    `tfsdk:"keepers"`
	RotationGracePeriodSeconds types.Int64  `tfsdk:"rotation_grace_period_seconds"`
	PreviousSecretExpiresAt    types.String `tfsdk:"previous_secret_expires_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An API key of the Exa team that owns the provider's service key. " +
			"The key's secret is stored in the Terraform state, so protect the state accordingly.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The key's public ID (`key_…`), Exa's `publicId`. It does not change when the key is rotated. " +
					"Not the `id` field of Exa's API responses, which holds the secret.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the key, shown in the Exa dashboard.",
				Required:            true,
			},
			"rate_limit": schema.Int64Attribute{
				MarkdownDescription: "Requests per second allowed for the key. Leave it out for no key-specific limit. " +
					"Exa cannot remove a rate limit from an existing key, so removing this attribute replaces the key, which gets a new secret.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplaceIf(
						func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
							resp.RequiresReplace = !req.StateValue.IsNull() && req.ConfigValue.IsNull()
						},
						"Removing the rate limit replaces the key.",
						"Removing the rate limit replaces the key.",
					),
				},
			},
			"budget_cents": schema.Int64Attribute{
				MarkdownDescription: "Spending budget for the key in the team's current billing period, in cents. It resets each billing period. " +
					"No budget by default, and removing it removes the budget. Requires the API key budgets feature on the Exa team: without it, Exa rejects the request.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
			"secret": schema.StringAttribute{
				MarkdownDescription: "The API key to send as `x-api-key` to the Exa API.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"team_id": schema.StringAttribute{
				MarkdownDescription: "ID of the Exa team the key belongs to.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"keepers": schema.MapAttribute{
				MarkdownDescription: "Values that rotate the key's secret when they change, such as the `id` of a `time_rotating`. " +
					"Unlike `keepers` in other providers, a change doesn't replace the key: the secret rotates in place, the key keeps its public ID (`id`) and usage history, " +
					"and the old secret keeps working for `rotation_grace_period_seconds`.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"rotation_grace_period_seconds": schema.Int64Attribute{
				MarkdownDescription: "How long the old secret keeps working after a rotation, in seconds, at most 90 days. 0 revokes it immediately. " +
					"Required with `keepers`, no default. Exa refuses to rotate again before it expires.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(0, 7776000)},
			},
			"previous_secret_expires_at": schema.StringAttribute{
				MarkdownDescription: "When the secret replaced by the last rotation stops working, or null once it has.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the key was created (RFC 3339).",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *apiKeyResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.RequiredTogether(path.MatchRoot("keepers"), path.MatchRoot("rotation_grace_period_seconds")),
	}
}

func (r *apiKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Keepers.Equal(state.Keepers) {
		return
	}
	plan.Secret = types.StringUnknown()
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*exa.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *exa.Client, got %T.", req.ProviderData))
		return
	}
	r.client = client
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.CreateAPIKey(ctx, exa.CreateAPIKeyRequest{
		Name:        plan.Name.ValueStringPointer(),
		RateLimit:   plan.RateLimit.ValueInt64Pointer(),
		BudgetCents: plan.BudgetCents.ValueInt64Pointer(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Exa API key", err.Error())
		return
	}

	plan.fill(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.GetAPIKey(ctx, state.ID.ValueString())
	if exa.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read Exa API key", err.Error())
		return
	}

	state.fill(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Teams without the budgets feature get a 403 for any budgetCents, even null.
	var update exa.UpdateAPIKeyRequest
	if !plan.Name.Equal(state.Name) {
		update.Name = plan.Name.ValueStringPointer()
	}
	if !plan.RateLimit.Equal(state.RateLimit) {
		update.RateLimit = plan.RateLimit.ValueInt64Pointer()
	}
	if !plan.BudgetCents.Equal(state.BudgetCents) {
		update.BudgetCents = plan.BudgetCents.ValueInt64Pointer()
		update.RemoveBudget = plan.BudgetCents.IsNull()
	}

	if update != (exa.UpdateAPIKeyRequest{}) {
		key, err := r.client.UpdateAPIKey(ctx, state.ID.ValueString(), update)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update Exa API key", err.Error())
			return
		}
		plan.fill(key)
	}

	if !plan.Keepers.Equal(state.Keepers) {
		key, err := r.client.RotateAPIKey(ctx, state.ID.ValueString(), plan.RotationGracePeriodSeconds.ValueInt64())
		if err != nil {
			// Keep the old triggers so the next apply retries the rotation.
			saved := plan
			saved.Keepers = state.Keepers
			saved.Secret = state.Secret
			saved.PreviousSecretExpiresAt = state.PreviousSecretExpiresAt
			resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
			resp.Diagnostics.AddError("Unable to rotate Exa API key", rotateErrorDetail(err, state))
			return
		}
		plan.fill(key)
	}

	if plan.Secret.IsUnknown() {
		plan.Secret = state.Secret
	}
	if plan.PreviousSecretExpiresAt.IsUnknown() {
		plan.PreviousSecretExpiresAt = state.PreviousSecretExpiresAt
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func rotateErrorDetail(err error, state apiKeyModel) string {
	var apiErr *exa.Error
	if errors.As(err, &apiErr) && apiErr.Code == "previous_secret_active" {
		return fmt.Sprintf("Exa refuses to rotate while the secret replaced by the last rotation is still in its grace period (until %s). Apply again once it has expired.",
			state.PreviousSecretExpiresAt.ValueString())
	}
	return err.Error()
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteAPIKey(ctx, state.ID.ValueString())
	if err != nil && !exa.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete Exa API key", err.Error())
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (m *apiKeyModel) fill(k *exa.APIKey) {
	// The API also accepts the secret as an ID; never keep it in id.
	m.ID = types.StringValue(k.PublicID)
	m.Name = types.StringValue(k.Name)
	m.RateLimit = types.Int64PointerValue(k.RateLimit)
	m.BudgetCents = types.Int64PointerValue(k.BudgetCents)
	setIfPresent(&m.Secret, k.Secret)
	setIfPresent(&m.TeamID, k.TeamID)
	setIfPresent(&m.CreatedAt, k.CreatedAt)
	m.PreviousSecretExpiresAt = types.StringPointerValue(k.PreviousSecretExpiresAt)
}

func setIfPresent(dst *types.String, v string) {
	if v != "" {
		*dst = types.StringValue(v)
	}
}
