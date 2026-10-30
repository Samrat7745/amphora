// Package client is the Go replacement for amphora-java-client.
//
// Java -> Go
//
//	AmphoraClient.createSecret / getSecret / deleteSecret              -> Client.CreateSecret / GetSecret / DeleteSecret
//	AmphoraClient.getSecrets(...) (filter / sort / page variants)      -> Client.GetSecrets (list) / Client.GetSecretsPage
//	AmphoraClient.getTags / createTag / overwriteTags / getTag /
//	  updateTag / deleteTag                                            -> Client.GetTags / CreateTag / OverwriteTags / GetTag / UpdateTag / DeleteTag
//	AmphoraIntraVcpClient.uploadSecretShare / getSecretShare           -> IntraVcpClient.UploadSecretShare / GetSecretShare
//	AmphoraInterVcpClient.open                                         -> InterVcpClient.Open
//	DefaultAmphoraClient.builder() ...                                 -> New(Options)
//
// Secrets are secret shared across all configured Amphora services: CreateSecret downloads and verifies input masks from
// every service, uploads the masked input to each, and GetSecret downloads the shares from every service, verifies the
// MACs/multiplication triples and recombines the plaintext. Failures are *ClientError; a failed integrity check is
// *IntegrityError (Java IntegrityVerificationException).
package client
