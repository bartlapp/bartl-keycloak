package native

const UpgradeSourceVersion = "26.6.1"
const UpgradeSourceImage = "quay.io/keycloak/keycloak:26.6.1@sha256:dea26401d06341095cc4ea9d66896200b55de5ca1daa1d2fcbe58493afa6e0ad"
const KeycloakImage = "quay.io/keycloak/keycloak:26.6.2@sha256:f9ba7b2af90db8dc749a57ca9aedca51e840cb9224441ab546a968da941da900"

// The supported source pair uses one native destination startup; no intermediate
// Keycloak version is required. Restore admits it only on independent target data.
func compatibleRecoveryImage(source, target string) bool {
	return target == KeycloakImage && (source == KeycloakImage || source == UpgradeSourceImage)
}
