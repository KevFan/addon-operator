package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonsv1alpha1 "github.com/openshift/addon-operator/apis/addons/v1alpha1"
	"github.com/openshift/addon-operator/integration"
	"github.com/openshift/addon-operator/internal/controllers/addon"
	"github.com/openshift/addon-operator/internal/featuretoggle"

	"package-operator.run/apis/core/v1alpha1"
	pkov1alpha1 "package-operator.run/apis/core/v1alpha1"
)

const (
	addonName              = "addonname-pko-boatboat"
	addonNamespace         = "namespace-onbgdions"
	clusterIDValue         = "a440b136-b2d6-406b-a884-fca2d62cd170"
	deadMansSnitchUrlValue = "https://example.com/test-snitch-url"
	ocmClusterIDValue      = "foobar"
	pagerDutyKeyValue      = "1234567890ABCDEF"

	// source: https://github.com/kostola/package-operator-packages/tree/v2.0/openshift/addon-operator/apnp-test-optional-params
	pkoImageOptionalParams = "quay.io/alcosta/package-operator-packages/openshift/addon-operator/apnp-test-optional-params:v2.0"
	// source: https://github.com/kostola/package-operator-packages/tree/v2.0/openshift/addon-operator/apnp-test-required-params
	pkoImageRequiredParams = "quay.io/alcosta/package-operator-packages/openshift/addon-operator/apnp-test-required-params:v2.0"
)

func (s *integrationTestSuite) TestPackageOperatorReconcilerStatusPropagatedToAddon() {
	if !featuretoggle.IsEnabledOnTestEnv(&featuretoggle.AddonsPlugAndPlayFeatureToggle{}) {
		s.T().Skip("skipping PackageOperatorReconciler integration tests as the feature toggle for it is disabled in the test environment")
	}

	ctx := context.Background()

	name := "addonname-pko-boatboat"

	image := "nonExistantImage"
	namespace := "redhat-reference-addon" // This namespace is hard coded in managed tenants bundles

	addon := &addonsv1alpha1.Addon{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: addonsv1alpha1.AddonSpec{
			Version:              "1.0",
			DisplayName:          name,
			AddonPackageOperator: &addonsv1alpha1.AddonPackageOperator{Image: image},
			Namespaces:           []addonsv1alpha1.AddonNamespace{{Name: namespace}},
			Install: addonsv1alpha1.AddonInstallSpec{
				Type: addonsv1alpha1.OLMOwnNamespace,
				OLMOwnNamespace: &addonsv1alpha1.AddonInstallOLMOwnNamespace{
					AddonInstallOLMCommon: addonsv1alpha1.AddonInstallOLMCommon{
						Namespace:          namespace,
						CatalogSourceImage: referenceAddonCatalogSourceImageWorking,
						Channel:            "alpha",
						PackageName:        "reference-addon",
						Config:             &addonsv1alpha1.SubscriptionConfig{EnvironmentVariables: referenceAddonConfigEnvObjects},
					},
				},
			},
		},
	}

	tmpl := &pkov1alpha1.ClusterObjectTemplate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}

	err := integration.Client.Create(ctx, addon)
	s.Require().NoError(err)
	// wait until ClusterObjectTemplate is created
	err = integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, tmpl, "to be created",
		func(obj client.Object) (done bool, err error) {
			_ = obj.(*pkov1alpha1.ClusterObjectTemplate)
			return true, nil
		})
	s.Require().NoError(err)

	err = integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, addon, "to be unavailable",
		func(obj client.Object) (done bool, err error) {
			addonBrokenImage := obj.(*addonsv1alpha1.Addon)
			availableCondition := meta.FindStatusCondition(addonBrokenImage.Status.Conditions, addonsv1alpha1.Available)
			done = availableCondition.Status == metav1.ConditionFalse &&
				availableCondition.Reason == addonsv1alpha1.AddonReasonUnreadyClusterPackageTemplate
			return done, nil
		})
	s.Require().NoError(err)

	// Patch image
	patchedImage := "quay.io/osd-addons/reference-addon-package:56916cb"
	patch := fmt.Sprintf(`{"spec":{"packageOperator":{"image":"%s"}}}`, patchedImage)
	err = integration.Client.Patch(ctx, addon, client.RawPatch(types.MergePatchType, []byte(patch)))
	s.Require().NoError(err)

	// wait until ClusterObjectTemplate image is patched and is available
	err = integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, tmpl, "to be patched",
		func(obj client.Object) (done bool, err error) {
			clusterObjectTemplate := obj.(*pkov1alpha1.ClusterObjectTemplate)
			if !strings.Contains(clusterObjectTemplate.Spec.Template, patchedImage) {
				return false, nil
			}
			meta.IsStatusConditionTrue(clusterObjectTemplate.Status.Conditions, pkov1alpha1.PackageAvailable)
			return true, nil
		})
	s.Require().NoError(err)

	err = integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, addon, "to be available",
		func(obj client.Object) (done bool, err error) {
			addonAfterPatch := obj.(*addonsv1alpha1.Addon)
			availableCondition := meta.FindStatusCondition(addonAfterPatch.Status.Conditions, addonsv1alpha1.Available)
			done = availableCondition.Status == metav1.ConditionTrue
			return done, nil
		})
	s.Require().NoError(err)

	s.T().Cleanup(func() { s.addonCleanup(addon, ctx) })
}

func (s *integrationTestSuite) TestPackageOperatorReconcilerSourceParameterInjection() {
	if !featuretoggle.IsEnabledOnTestEnv(&featuretoggle.AddonsPlugAndPlayFeatureToggle{}) {
		s.T().Skip("skipping PackageOperatorReconciler integration tests as the feature toggle for it is disabled in the test environment")
	}

	tests := []struct {
		name                        string
		pkoImage                    string
		deployAddonParametersSecret bool
		deployDeadMansSnitchSecret  bool
		deployPagerDutySecret       bool
		clusterPackageStatus        string
	}{
		{
			"OptionalParamsAllMissing", pkoImageOptionalParams,
			false, false, false,
			v1alpha1.PackageAvailable,
		},
		{
			"OptionalParams1stMissing", pkoImageOptionalParams,
			false, true, true,
			v1alpha1.PackageAvailable,
		},
		{
			"OptionalParams2ndMissing", pkoImageOptionalParams,
			true, false, true,
			v1alpha1.PackageAvailable,
		},
		{
			"OptionalParams3rdMissing", pkoImageOptionalParams,
			true, true, false,
			v1alpha1.PackageAvailable,
		},
		{
			"OptionalParamsAllPresent", pkoImageOptionalParams,
			true, true, true,
			v1alpha1.PackageAvailable,
		},
		{
			"RequiredParamsAllMissing", pkoImageRequiredParams,
			false, false, false,
			v1alpha1.PackageInvalid,
		},
		{
			"RequiredParams1stMissing", pkoImageRequiredParams,
			false, true, true,
			v1alpha1.PackageInvalid,
		},
		{
			"RequiredParams2ndMissing", pkoImageRequiredParams,
			true, false, true,
			v1alpha1.PackageInvalid,
		},
		{
			"RequiredParams3rdMissing", pkoImageRequiredParams,
			true, true, false,
			v1alpha1.PackageInvalid,
		},
		{
			"RequiredParamsAllPresent", pkoImageRequiredParams,
			true, true, true,
			v1alpha1.PackageAvailable,
		},
	}

	for index, test := range tests {
		s.Run(test.name, func() {
			testAddonName := fmt.Sprintf("%s-%d", addonName, index)
			testAddonNamespace := fmt.Sprintf("%s-%d", addonNamespace, index)
			ctx := context.Background()

			addon := s.createAddon(ctx, testAddonName, testAddonNamespace, test.pkoImage)
			s.waitForNamespace(ctx, testAddonNamespace)

			if test.deployAddonParametersSecret {
				s.createAddonParametersSecret(ctx, testAddonName, testAddonNamespace)
			}
			if test.deployDeadMansSnitchSecret {
				s.createDeadMansSnitchSecret(ctx, testAddonName, testAddonNamespace)
			}
			if test.deployPagerDutySecret {
				s.createPagerDutySecret(ctx, testAddonName, testAddonNamespace)
			}

			s.waitForClusterPackage(
				ctx,
				testAddonName,
				testAddonNamespace,
				test.clusterPackageStatus,
				test.deployAddonParametersSecret,
				test.deployDeadMansSnitchSecret,
				test.deployPagerDutySecret,
			)

			s.T().Cleanup(func() { s.addonCleanup(addon, ctx) })
		})
	}
}

// create the Addon resource
func (s *integrationTestSuite) createAddon(ctx context.Context, addonName string, addonNamespace string, pkoImage string) *addonsv1alpha1.Addon {
	addon := &addonsv1alpha1.Addon{
		ObjectMeta: metav1.ObjectMeta{Name: addonName},
		Spec: addonsv1alpha1.AddonSpec{
			Version:              "1.0",
			DisplayName:          addonName,
			AddonPackageOperator: &addonsv1alpha1.AddonPackageOperator{Image: pkoImage},
			Namespaces:           []addonsv1alpha1.AddonNamespace{{Name: addonNamespace}},
			Install: addonsv1alpha1.AddonInstallSpec{
				Type: addonsv1alpha1.OLMOwnNamespace,
				OLMOwnNamespace: &addonsv1alpha1.AddonInstallOLMOwnNamespace{
					AddonInstallOLMCommon: addonsv1alpha1.AddonInstallOLMCommon{
						Namespace:          addonNamespace,
						CatalogSourceImage: referenceAddonCatalogSourceImageWorking,
						Channel:            "alpha",
						PackageName:        "reference-addon",
						Config:             &addonsv1alpha1.SubscriptionConfig{EnvironmentVariables: referenceAddonConfigEnvObjects},
					},
				},
			},
		},
	}

	err := integration.Client.Create(ctx, addon)
	s.Require().NoError(err)

	return addon
}

// wait for the Addon addonNamespace to exist (needed to publish secrets)
func (s *integrationTestSuite) waitForNamespace(ctx context.Context, addonNamespace string) {
	err := integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: addonNamespace}}, "to be created",
		func(obj client.Object) (done bool, err error) { return true, nil })
	s.Require().NoError(err)
}

// create the Secret resource for Addon Parameters
func (s *integrationTestSuite) createAddonParametersSecret(ctx context.Context, addonName string, addonNamespace string) {
	s.createSecret(ctx, "addon-"+addonName+"-parameters", addonNamespace, map[string][]byte{"foo1": []byte("bar"), "foo2": []byte("baz")})
}

// create the Secret resource for Dead Man's Snitch as defined here:
// - https://mt-sre.github.io/docs/creating-addons/monitoring/deadmanssnitch_integration/#generated-secret
func (s *integrationTestSuite) createDeadMansSnitchSecret(ctx context.Context, addonName string, addonNamespace string) {
	s.createSecret(ctx, addonName+"-deadmanssnitch", addonNamespace, map[string][]byte{"SNITCH_URL": []byte(deadMansSnitchUrlValue)})
}

// create the Secret resource for PagerDuty as defined here:
// - https://mt-sre.github.io/docs/creating-addons/monitoring/pagerduty_integration/
func (s *integrationTestSuite) createPagerDutySecret(ctx context.Context, addonName string, addonNamespace string) {
	s.createSecret(ctx, addonName+"-pagerduty", addonNamespace, map[string][]byte{"PAGERDUTY_KEY": []byte(pagerDutyKeyValue)})
}

func (s *integrationTestSuite) createSecret(ctx context.Context, name string, namespace string, data map[string][]byte) {
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
	err := integration.Client.Create(ctx, secret)
	s.Require().NoError(err)
}

// wait until all the replicas in the Deployment inside the ClusterPackage are ready
// and check if their env variables corresponds to the secrets
func (s *integrationTestSuite) waitForClusterPackage(ctx context.Context, addonName string, addonNamespace string, conditionType string,
	addonParametersValuePresent bool, deadMansSnitchUrlValuePresent bool, pagerDutyValuePresent bool,
) {
	cp := &v1alpha1.ClusterPackage{ObjectMeta: metav1.ObjectMeta{Name: addonName}}
	err := integration.WaitForObject(ctx, s.T(),
		defaultAddonAvailabilityTimeout, cp, "to be "+conditionType,
		clusterPackageChecker(addonNamespace, conditionType, addonParametersValuePresent, deadMansSnitchUrlValuePresent, pagerDutyValuePresent))
	s.Require().NoError(err)
}

func clusterPackageChecker(
	addonNamespace string,
	conditionType string,
	addonParametersValuePresent bool,
	deadMansSnitchUrlValuePresent bool,
	pagerDutyValuePresent bool,
) func(client.Object) (done bool, err error) {
	if conditionType == v1alpha1.PackageInvalid {
		return func(obj client.Object) (done bool, err error) {
			clusterPackage := obj.(*v1alpha1.ClusterPackage)
			return meta.IsStatusConditionTrue(clusterPackage.Status.Conditions, conditionType), nil
		}
	}

	return func(obj client.Object) (done bool, err error) {
		clusterPackage := obj.(*v1alpha1.ClusterPackage)
		if !meta.IsStatusConditionTrue(clusterPackage.Status.Conditions, conditionType) {
			return false, nil
		}

		config := make(map[string]map[string]interface{})
		if err := json.Unmarshal(clusterPackage.Spec.Config.Raw, &config); err != nil {
			return false, err
		}

		addonsv1, present := config["addonsv1"]
		if !present {
			return false, nil
		}

		targetNamespace, present := addonsv1[addon.TargetNamespaceConfigKey]
		targetNamespaceValueOk := present && targetNamespace == addonNamespace

		clusterID, present := addonsv1[addon.ClusterIDConfigKey]
		clusterIDValueOk := present && clusterID == clusterIDValue

		ocmClusterID, present := addonsv1[addon.OcmClusterIDConfigKey]
		ocmClusterIDValueOk := present && ocmClusterID == ocmClusterIDValue

		addonParametersValueOk, deadMansSnitchUrlValueOk, pagerDutyValueOk := false, false, false
		if addonParametersValuePresent {
			value, present := addonsv1[addon.AddonParametersConfigKey]
			if present {
				jsonValue, err := json.Marshal(value)
				if err == nil {
					addonParametersValueOk = string(jsonValue) == "{\"foo1\":\"YmFy\",\"foo2\":\"YmF6\"}"
				}
			}
		} else {
			_, present := addonsv1[addon.AddonParametersConfigKey]
			addonParametersValueOk = !present
		}
		if deadMansSnitchUrlValuePresent {
			value, present := addonsv1[addon.DeadMansSnitchUrlConfigKey]
			deadMansSnitchUrlValueOk = present && fmt.Sprint(value) == base64.StdEncoding.EncodeToString([]byte(deadMansSnitchUrlValue))
		} else {
			_, present := addonsv1[addon.DeadMansSnitchUrlConfigKey]
			deadMansSnitchUrlValueOk = !present
		}
		if pagerDutyValuePresent {
			value, present := addonsv1[addon.PagerDutyKeyConfigKey]
			pagerDutyValueOk = present && fmt.Sprint(value) == base64.StdEncoding.EncodeToString([]byte(pagerDutyKeyValue))
		} else {
			_, present := addonsv1[addon.PagerDutyKeyConfigKey]
			pagerDutyValueOk = !present
		}

		return targetNamespaceValueOk &&
			clusterIDValueOk &&
			ocmClusterIDValueOk &&
			addonParametersValueOk &&
			deadMansSnitchUrlValueOk &&
			pagerDutyValueOk, nil
	}
}
