package jobs

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
)

type ProviderProcessor struct {
	client *provider.Client
}

func NewProviderProcessor(client *provider.Client) *ProviderProcessor {
	return &ProviderProcessor{client: client}
}

func (p *ProviderProcessor) Enabled() bool { return p != nil && p.client != nil && p.client.Enabled() }

func (p *ProviderProcessor) Identity() string { return p.client.Identity() }

func (p *ProviderProcessor) Process(ctx context.Context, input domain.ProcessingInput) domain.ProcessingResult {
	request := providerRequest(input)
	output, err := p.client.Process(ctx, request)
	return p.result(input, output, err)
}

func (p *ProviderProcessor) ProcessGraph(ctx context.Context, input domain.GraphExtractionInput) domain.GraphExtractionResult {
	request := provider.NewGraphExtractRequest(input.ProcessorIdentity, input.ProcessingVersion, input.JobID, input.Attempt, input.SpaceID, input.MemoryID, input.RevisionID, input.Content, input.Sensitivity, providerMemories(input.ContextMemories), input.Policy.MaxEntities, input.Policy.MaxFacts, input.Policy.MaxRelations, input.Policy.MaxContentChars)
	output, err := p.client.GraphExtract(ctx, request)
	if err != nil {
		result := domain.GraphExtractionResult{Code: domain.GraphExtractionPermanentFailure, Message: "provider graph extraction failed", ProviderIdentity: p.client.Identity(), ModelIdentity: p.client.ModelIdentity()}
		var providerErr *provider.Error
		if errors.As(err, &providerErr) {
			result.ReasonCode = providerErr.Code()
			switch providerErr.Class() {
			case provider.ErrorDisabled:
				result.Code = domain.GraphExtractionNoProcessor
			case provider.ErrorPolicy:
				result.Code = domain.GraphExtractionPolicyRejected
			case provider.ErrorTransient:
				result.Code = domain.GraphExtractionTransientFailure
			case provider.ErrorInvalidInput:
				result.Code = domain.GraphExtractionPermanentFailure
			case provider.ErrorInvalidOutput:
				result.Code = domain.GraphExtractionInvalidOutput
			}
		}
		return result
	}
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted, ProviderIdentity: p.client.Identity(), ModelIdentity: p.client.ModelIdentity(), ProviderRequestID: output.ProviderRequestID(), Usage: domain.ProcessingUsage{InputTokens: output.Usage().InputTokens(), OutputTokens: output.Usage().OutputTokens()}}
	for _, entity := range output.Entities() {
		result.Entities = append(result.Entities, domain.GraphEntity{LocalID: entity.LocalID, CanonicalName: entity.CanonicalName, EntityType: entity.EntityType, Aliases: entity.Aliases, Confidence: entity.Confidence})
	}
	for _, mention := range output.Mentions() {
		result.Mentions = append(result.Mentions, domain.GraphMention{EntityLocalID: mention.EntityLocalID, Text: mention.Text, StartRune: mention.StartRune, EndRune: mention.EndRune, Confidence: mention.Confidence})
	}
	for _, fact := range output.Facts() {
		item := domain.GraphFact{SubjectLocalID: fact.SubjectLocalID, Predicate: fact.Predicate, ObjectLocalID: fact.ObjectLocalID, ObjectLiteral: fact.ObjectLiteral, ObjectLiteralType: fact.ObjectLiteralType, Confidence: fact.Confidence}
		if fact.ValidTime != nil {
			item.ValidFrom, item.ValidUntil = fact.ValidTime.Start, fact.ValidTime.End
		}
		result.Facts = append(result.Facts, item)
	}
	for _, relation := range output.Relations() {
		result.Relations = append(result.Relations, domain.GraphMemoryRelation{RelationType: relation.RelationType, TargetMemoryID: relation.TargetMemoryID, TargetRevisionID: relation.TargetRevisionID, Confidence: relation.Confidence})
	}
	if len(result.Entities) == 0 && len(result.Mentions) == 0 && len(result.Facts) == 0 && len(result.Relations) == 0 {
		result.Code = domain.GraphExtractionEmpty
	}
	return result
}

func (p *ProviderProcessor) Preflight(input domain.ProcessingInput) *domain.ProcessingResult {
	err := p.client.PreflightProcess(providerRequest(input))
	if err == nil {
		return nil
	}
	result := p.result(input, provider.ProcessResult{}, err)
	return &result
}

func providerRequest(input domain.ProcessingInput) provider.ProcessRequest {
	kinds := make([]domain.MemoryKind, 0, len(input.Policy.AllowedMemoryTypes))
	for _, memoryType := range input.Policy.AllowedMemoryTypes {
		kinds = append(kinds, domain.MemoryKind(memoryType))
	}
	return provider.NewProcessRequest(
		input.ProcessorIdentity, input.ProcessingVersion, input.JobID, input.Attempt,
		providerEvent(input.Target), providerEvents(input.ContextEvents), providerMemories(input.ContextMemories), kinds, input.Policy.MaxCandidates, input.Policy.MaxCandidateChars, input.Policy.MaxTotalCandidateChars,
	)
}

func (p *ProviderProcessor) result(input domain.ProcessingInput, output provider.ProcessResult, err error) domain.ProcessingResult {
	if err != nil {
		result := domain.ProcessingResult{Code: domain.ProcessingPermanentFailure, Message: "provider request failed", ProviderIdentity: p.client.Identity(), ModelIdentity: p.client.ModelIdentity(), PolicyVersion: p.client.EgressPolicyVersion()}
		var providerErr *provider.Error
		if errors.As(err, &providerErr) {
			result.ReasonCode = providerErr.Code()
			switch providerErr.Class() {
			case provider.ErrorDisabled:
				result.Code = domain.ProcessingNoProcessor
			case provider.ErrorPolicy:
				result.Code = domain.ProcessingPolicyRejected
			case provider.ErrorTransient:
				result.Code = domain.ProcessingTransientFailure
			case provider.ErrorInvalidInput, provider.ErrorInvalidOutput:
				result.Code = domain.ProcessingInvalidOutput
			}
		}
		return result
	}
	result := domain.ProcessingResult{
		Code: domain.ProcessingOutcome(output.Category()), ProviderIdentity: p.client.Identity(), ModelIdentity: p.client.ModelIdentity(),
		PolicyVersion: p.client.EgressPolicyVersion(), ProviderRequestID: output.ProviderRequestID(),
		Usage:               domain.ProcessingUsage{InputTokens: output.Usage().InputTokens(), OutputTokens: output.Usage().OutputTokens()},
		OmittedContextCount: output.OmittedContextCount(), OmissionSummary: output.OmissionSummary(),
	}
	if result.OmissionSummary == nil {
		result.OmissionSummary = make(map[string]int)
	}
	for reason, count := range input.OmissionSummary {
		result.OmissionSummary[reason] += count
		result.OmittedContextCount += count
	}
	for _, revision := range output.Revisions() {
		result.Proposals = append(result.Proposals, domain.MemoryCandidateProposal{Revision: revision, Action: domain.ConsolidationCreate})
	}
	return result
}

func providerMemories(values []domain.ProcessingMemory) []provider.Memory {
	result := make([]provider.Memory, len(values))
	for i, value := range values {
		result[i] = provider.NewMemoryWithProposalCapability(value.MemoryID, value.RevisionID, value.SpaceID, value.Content, value.CurrentState, value.Sensitivity, value.ProposalCapable, value.ValidFrom, value.ValidUntil)
	}
	return result
}

func providerEvents(values []domain.ProcessingEvent) []provider.Event {
	result := make([]provider.Event, len(values))
	for i := range values {
		result[i] = providerEvent(values[i])
	}
	return result
}

func providerEvent(value domain.ProcessingEvent) provider.Event {
	var content provider.Content
	switch value.Content.Kind {
	case "json":
		content = provider.NewJSONContent(value.Content.Text)
	case "reference":
		content = provider.NewReferenceContent(value.Content.MediaType, value.Content.DeclaredSize, value.Content.DeclaredHash)
	default:
		content = provider.NewTextContent(value.Content.MediaType, value.Content.Text)
	}
	return provider.NewEvent(value.EventID, value.SpaceID, value.SourceType, value.SourceName, value.Sensitivity, value.OccurredAt, content)
}
