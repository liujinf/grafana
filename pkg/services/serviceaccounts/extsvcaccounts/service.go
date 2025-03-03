package extsvcaccounts

import (
	"context"
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/components/satokengen"
	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/slugify"
	"github.com/grafana/grafana/pkg/models/roletype"
	ac "github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/extsvcauth"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/services/pluginsintegration/pluginsettings"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/secrets/kvstore"
	sa "github.com/grafana/grafana/pkg/services/serviceaccounts"
	"github.com/grafana/grafana/pkg/services/serviceaccounts/manager"
)

type ExtSvcAccountsService struct {
	acSvc    ac.Service
	features *featuremgmt.FeatureManager
	logger   log.Logger
	metrics  *metrics
	saSvc    sa.Service
	skvStore kvstore.SecretsKVStore
}

func ProvideExtSvcAccountsService(acSvc ac.Service, bus bus.Bus, db db.DB, features *featuremgmt.FeatureManager, reg prometheus.Registerer, saSvc *manager.ServiceAccountsService, secretsSvc secrets.Service) *ExtSvcAccountsService {
	logger := log.New("serviceauth.extsvcaccounts")
	esa := &ExtSvcAccountsService{
		acSvc:    acSvc,
		logger:   logger,
		saSvc:    saSvc,
		features: features,
		skvStore: kvstore.NewSQLSecretsKVStore(db, secretsSvc, logger), // Using SQL store to avoid a cyclic dependency
	}

	if features.IsEnabled(featuremgmt.FlagExternalServiceAccounts) || features.IsEnabled(featuremgmt.FlagExternalServiceAuth) {
		// Register the metrics
		esa.metrics = newMetrics(reg, saSvc, logger)

		// Register a listener to enable/disable service accounts
		bus.AddEventListener(esa.handlePluginStateChanged)
	}

	return esa
}

// EnableExtSvcAccount enables or disables the service account associated to an external service
func (esa *ExtSvcAccountsService) EnableExtSvcAccount(ctx context.Context, cmd *sa.EnableExtSvcAccountCmd) error {
	saName := sa.ExtSvcPrefix + slugify.Slugify(cmd.ExtSvcSlug)

	saID, errRetrieve := esa.saSvc.RetrieveServiceAccountIdByName(ctx, cmd.OrgID, saName)
	if errRetrieve != nil {
		return errRetrieve
	}

	return esa.saSvc.EnableServiceAccount(ctx, cmd.OrgID, saID, cmd.Enabled)
}

// RetrieveExtSvcAccount fetches an external service account by ID
func (esa *ExtSvcAccountsService) RetrieveExtSvcAccount(ctx context.Context, orgID, saID int64) (*sa.ExtSvcAccount, error) {
	svcAcc, err := esa.saSvc.RetrieveServiceAccount(ctx, orgID, saID)
	if err != nil {
		return nil, err
	}
	return &sa.ExtSvcAccount{
		ID:         svcAcc.Id,
		Login:      svcAcc.Login,
		Name:       svcAcc.Name,
		OrgID:      svcAcc.OrgId,
		IsDisabled: svcAcc.IsDisabled,
		Role:       roletype.RoleType(svcAcc.Role),
	}, nil
}

// SaveExternalService creates, updates or delete a service account (and its token) with the requested permissions.
func (esa *ExtSvcAccountsService) SaveExternalService(ctx context.Context, cmd *extsvcauth.ExternalServiceRegistration) (*extsvcauth.ExternalService, error) {
	// This is double proofing, we should never reach here anyway the flags have already been checked.
	if !esa.features.IsEnabled(featuremgmt.FlagExternalServiceAccounts) && !esa.features.IsEnabled(featuremgmt.FlagExternalServiceAuth) {
		esa.logger.Warn("This feature is behind a feature flag, please set it if you want to save external services")
		return nil, nil
	}

	if cmd == nil {
		esa.logger.Warn("Received no input")
		return nil, nil
	}

	slug := slugify.Slugify(cmd.Name)

	if cmd.Impersonation.Enabled {
		esa.logger.Warn("Impersonation setup skipped. It is not possible to impersonate with a service account token.", "service", slug)
	}

	saID, err := esa.ManageExtSvcAccount(ctx, &sa.ManageExtSvcAccountCmd{
		ExtSvcSlug:  slug,
		Enabled:     cmd.Self.Enabled,
		OrgID:       extsvcauth.TmpOrgID,
		Permissions: cmd.Self.Permissions,
	})
	if err != nil {
		return nil, err
	}

	// No need for a token if we don't have a service account
	if saID <= 0 {
		esa.logger.Debug("Skipping service account token creation", "service", slug)
		return nil, nil
	}

	token, err := esa.getExtSvcAccountToken(ctx, extsvcauth.TmpOrgID, saID, slug)
	if err != nil {
		esa.logger.Error("Could not get the external svc token",
			"service", slug,
			"saID", saID,
			"error", err.Error())
		return nil, err
	}
	return &extsvcauth.ExternalService{Name: cmd.Name, ID: slug, Secret: token}, nil
}

// ManageExtSvcAccount creates, updates or deletes the service account associated with an external service
func (esa *ExtSvcAccountsService) ManageExtSvcAccount(ctx context.Context, cmd *sa.ManageExtSvcAccountCmd) (int64, error) {
	// This is double proofing, we should never reach here anyway the flags have already been checked.
	if !esa.features.IsEnabled(featuremgmt.FlagExternalServiceAccounts) && !esa.features.IsEnabled(featuremgmt.FlagExternalServiceAuth) {
		esa.logger.Warn("This feature is behind a feature flag, please set it if you want to save external services")
		return 0, nil
	}

	if cmd == nil {
		esa.logger.Warn("Received no input")
		return 0, nil
	}

	saID, errRetrieve := esa.saSvc.RetrieveServiceAccountIdByName(ctx, cmd.OrgID, sa.ExtSvcPrefix+cmd.ExtSvcSlug)
	if errRetrieve != nil && !errors.Is(errRetrieve, sa.ErrServiceAccountNotFound) {
		return 0, errRetrieve
	}

	if len(cmd.Permissions) == 0 {
		if saID > 0 {
			if err := esa.deleteExtSvcAccount(ctx, cmd.OrgID, cmd.ExtSvcSlug, saID); err != nil {
				esa.logger.Error("Error occurred while deleting service account",
					"service", cmd.ExtSvcSlug,
					"saID", saID,
					"error", err.Error())
				return 0, err
			}
			esa.metrics.deletedCount.Inc()
		}
		esa.logger.Info("Skipping service account creation, no permission",
			"service", cmd.ExtSvcSlug,
			"permission count", len(cmd.Permissions),
			"saID", saID)
		return 0, nil
	}

	saID, errSave := esa.saveExtSvcAccount(ctx, &saveCmd{
		Enabled:     cmd.Enabled,
		ExtSvcSlug:  cmd.ExtSvcSlug,
		OrgID:       cmd.OrgID,
		Permissions: cmd.Permissions,
		SaID:        saID,
	})
	if errSave != nil {
		esa.logger.Error("Could not save service account", "service", cmd.ExtSvcSlug, "error", errSave.Error())
		return 0, errSave
	}
	esa.metrics.savedCount.Inc()

	return saID, nil
}

// saveExtSvcAccount creates or updates the service account associated with an external service
func (esa *ExtSvcAccountsService) saveExtSvcAccount(ctx context.Context, cmd *saveCmd) (int64, error) {
	if cmd.SaID <= 0 {
		// Create a service account
		esa.logger.Debug("Create service account", "service", cmd.ExtSvcSlug, "orgID", cmd.OrgID)
		sa, err := esa.saSvc.CreateServiceAccount(ctx, cmd.OrgID, &sa.CreateServiceAccountForm{
			Name:       sa.ExtSvcPrefix + cmd.ExtSvcSlug,
			Role:       newRole(roletype.RoleNone),
			IsDisabled: newBool(false),
		})
		if err != nil {
			return 0, err
		}
		cmd.SaID = sa.Id
	}

	// Enable or disable the service account
	esa.logger.Debug("Set service account state", "service", cmd.ExtSvcSlug, "saID", cmd.SaID, "enabled", cmd.Enabled)
	if err := esa.saSvc.EnableServiceAccount(ctx, cmd.OrgID, cmd.SaID, cmd.Enabled); err != nil {
		return 0, err
	}

	// update the service account's permissions
	esa.logger.Debug("Update role permissions", "service", cmd.ExtSvcSlug, "saID", cmd.SaID)
	if err := esa.acSvc.SaveExternalServiceRole(ctx, ac.SaveExternalServiceRoleCommand{
		OrgID:             ac.GlobalOrgID,
		Global:            true,
		ExternalServiceID: cmd.ExtSvcSlug,
		ServiceAccountID:  cmd.SaID,
		Permissions:       cmd.Permissions,
	}); err != nil {
		return 0, err
	}

	return cmd.SaID, nil
}

// deleteExtSvcAccount deletes a service account by ID and removes its associated role
func (esa *ExtSvcAccountsService) deleteExtSvcAccount(ctx context.Context, orgID int64, slug string, saID int64) error {
	esa.logger.Info("Delete service account", "service", slug, "orgID", orgID, "saID", saID)
	if err := esa.saSvc.DeleteServiceAccount(ctx, orgID, saID); err != nil {
		return err
	}
	if err := esa.acSvc.DeleteExternalServiceRole(ctx, slug); err != nil {
		return err
	}
	return esa.DeleteExtSvcCredentials(ctx, orgID, slug)
}

// getExtSvcAccountToken get or create the token of an External Service
func (esa *ExtSvcAccountsService) getExtSvcAccountToken(ctx context.Context, orgID, saID int64, extSvcSlug string) (string, error) {
	// Get credentials from store
	credentials, err := esa.GetExtSvcCredentials(ctx, orgID, extSvcSlug)
	if err != nil && !errors.Is(err, ErrCredentialsNotFound) {
		return "", err
	}
	if credentials != nil {
		return credentials.Secret, nil
	}

	// Generate token
	esa.logger.Info("Generate new service account token", "service", extSvcSlug, "orgID", orgID)
	newKeyInfo, err := satokengen.New(extSvcSlug)
	if err != nil {
		return "", err
	}

	esa.logger.Debug("Add service account token", "service", extSvcSlug, "orgID", orgID)
	if _, err := esa.saSvc.AddServiceAccountToken(ctx, saID, &sa.AddServiceAccountTokenCommand{
		Name:  tokenNamePrefix + "-" + extSvcSlug,
		OrgId: orgID,
		Key:   newKeyInfo.HashedKey,
	}); err != nil {
		return "", err
	}

	if err := esa.SaveExtSvcCredentials(ctx, &SaveCredentialsCmd{
		ExtSvcSlug: extSvcSlug,
		OrgID:      orgID,
		Secret:     newKeyInfo.ClientSecret,
	}); err != nil {
		return "", err
	}

	return newKeyInfo.ClientSecret, nil
}

// GetExtSvcCredentials get the credentials of an External Service from an encrypted storage
func (esa *ExtSvcAccountsService) GetExtSvcCredentials(ctx context.Context, orgID int64, extSvcSlug string) (*Credentials, error) {
	esa.logger.Debug("Get service account token from skv", "service", extSvcSlug, "orgID", orgID)
	token, ok, err := esa.skvStore.Get(ctx, orgID, extSvcSlug, kvStoreType)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrCredentialsNotFound.Errorf("No credential found for in store %v", extSvcSlug)
	}
	return &Credentials{Secret: token}, nil
}

// SaveExtSvcCredentials stores the credentials of an External Service in an encrypted storage
func (esa *ExtSvcAccountsService) SaveExtSvcCredentials(ctx context.Context, cmd *SaveCredentialsCmd) error {
	esa.logger.Debug("Save service account token in skv", "service", cmd.ExtSvcSlug, "orgID", cmd.OrgID)
	return esa.skvStore.Set(ctx, cmd.OrgID, cmd.ExtSvcSlug, kvStoreType, cmd.Secret)
}

// DeleteExtSvcCredentials removes the credentials of an External Service from an encrypted storage
func (esa *ExtSvcAccountsService) DeleteExtSvcCredentials(ctx context.Context, orgID int64, extSvcSlug string) error {
	esa.logger.Debug("Delete service account token from skv", "service", extSvcSlug, "orgID", orgID)
	return esa.skvStore.Del(ctx, orgID, extSvcSlug, kvStoreType)
}

func (esa *ExtSvcAccountsService) handlePluginStateChanged(ctx context.Context, event *pluginsettings.PluginStateChangedEvent) error {
	esa.logger.Info("Plugin state changed", "pluginId", event.PluginId, "enabled", event.Enabled)

	errEnable := esa.EnableExtSvcAccount(ctx, &sa.EnableExtSvcAccountCmd{
		ExtSvcSlug: event.PluginId,
		Enabled:    event.Enabled,
		OrgID:      extsvcauth.TmpOrgID,
	})

	// Ignore service account not found error
	if errors.Is(errEnable, sa.ErrServiceAccountNotFound) {
		esa.logger.Debug("No ext svc account with this plugin", "pluginId", event.PluginId)
		return nil
	}
	return errEnable
}
