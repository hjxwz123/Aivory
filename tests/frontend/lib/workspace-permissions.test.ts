import { describe, expect, it } from 'vitest'
import {
  canEditWorkspaceMemberPermissions,
  memberCanCreate,
  memberCanUse,
  workspaceCapabilities,
  workspaceCapabilitiesForScope,
  workspaceModelPolicyKey,
  workspacePolicyResolvedForScope,
} from '@/lib/workspace-permissions'

describe('workspace member permission semantics', () => {
  it('does not expose a member-permission editor for owners or admins', () => {
    expect(canEditWorkspaceMemberPermissions({ is_owner: true, role: 'admin' })).toBe(false)
    expect(canEditWorkspaceMemberPermissions({ is_owner: false, role: 'admin' })).toBe(false)
    expect(canEditWorkspaceMemberPermissions({ is_owner: false, role: 'member' })).toBe(true)
    expect(canEditWorkspaceMemberPermissions({ is_owner: false, role: 'guest' })).toBe(true)
  })

  it('keeps granular creation and usage rights independent', () => {
    const member = {
      can_create_skills_prompts: false,
      can_create_prompts: true,
      can_create_skills: false,
      can_create_mcp: true,
      can_use_prompts: false,
      can_use_skills: true,
      can_use_mcp: false,
    }
    expect(memberCanCreate(member, 'prompt')).toBe(true)
    expect(memberCanCreate(member, 'skill')).toBe(false)
    expect(memberCanCreate(member, 'mcp')).toBe(true)
    expect(memberCanUse(member, 'prompt')).toBe(false)
    expect(memberCanUse(member, 'skill')).toBe(true)
    expect(memberCanUse(member, 'mcp')).toBe(false)
  })

  it('falls back to the legacy aggregate creation bit when granular fields are absent', () => {
    const legacy = { can_create_skills_prompts: false }
    expect(memberCanCreate(legacy, 'prompt')).toBe(false)
    expect(memberCanCreate(legacy, 'skill')).toBe(false)
    expect(memberCanCreate(legacy, 'mcp')).toBe(false)
  })

  it('maps retired workspace policy switches conservatively during rollout', () => {
    expect(workspaceCapabilities({ AllowSandbox: false, AllowImageGeneration: true })).toMatchObject({
      toolCalling: false,
      drawing: true,
    })
    expect(workspaceCapabilities({ AllowSandbox: true, AllowImageGeneration: false })).toMatchObject({
      toolCalling: false,
      drawing: false,
    })
    // New fields always win when both generations are present.
    expect(workspaceCapabilities({
      AllowSandbox: false,
      AllowImageGeneration: false,
      AllowToolCalling: true,
      AllowDrawing: true,
    })).toMatchObject({ toolCalling: true, drawing: true })
  })

  it('fails closed for a known workspace until its policy is available', () => {
    expect(workspaceCapabilitiesForScope('workspace-1', undefined)).toEqual({
      toolCalling: false,
      drawing: false,
      mcp: false,
      skills: false,
      prompts: false,
      knowledgeBases: false,
      fileUpload: false,
    })
    expect(workspaceCapabilitiesForScope('workspace-1', null, { policyLoading: false })).toEqual({
      toolCalling: false,
      drawing: false,
      mcp: false,
      skills: false,
      prompts: false,
      knowledgeBases: false,
      fileUpload: false,
    })
  })

  it('fails closed when a cached workspace policy refresh reports an error', () => {
    expect(workspaceCapabilitiesForScope(
      'workspace-1',
      { AllowToolCalling: true, AllowDrawing: true },
      { policyError: 'network_error' },
    )).toEqual({
      toolCalling: false,
      drawing: false,
      mcp: false,
      skills: false,
      prompts: false,
      knowledgeBases: false,
      fileUpload: false,
    })
  })

  it('fails closed while workspace data or a cached policy is changing', () => {
    const enabled = { AllowToolCalling: true, AllowDrawing: true }
    const disabled = {
      toolCalling: false,
      drawing: false,
      mcp: false,
      skills: false,
      prompts: false,
      knowledgeBases: false,
      fileUpload: false,
    }
    expect(workspaceCapabilitiesForScope('workspace-1', enabled, {
      workspacesLoaded: false,
    })).toEqual(disabled)
    expect(workspaceCapabilitiesForScope('workspace-1', enabled, {
      policyLoading: true,
    })).toEqual(disabled)
    expect(workspaceCapabilitiesForScope('workspace-1', enabled, {
      switching: true,
    })).toEqual(disabled)
  })

  it('keeps personal space permissive regardless of workspace policy state', () => {
    expect(workspaceCapabilitiesForScope(null, undefined, { policyError: 'network_error' })).toEqual(
      workspaceCapabilities(undefined),
    )
    expect(workspaceCapabilitiesForScope(undefined, null, { policyLoading: true })).toEqual(
      workspaceCapabilities(null),
    )
  })

  it('uses the workspace policy once it is hydrated', () => {
    expect(workspaceCapabilitiesForScope('workspace-1', {
      AllowToolCalling: false,
      AllowDrawing: true,
      AllowMCP: false,
      AllowSkills: true,
      AllowPrompts: false,
      AllowKnowledgeBases: true,
      AllowFileUpload: false,
    })).toEqual({
      toolCalling: false,
      drawing: true,
      mcp: false,
      skills: true,
      prompts: false,
      knowledgeBases: true,
      fileUpload: false,
    })
  })

  it('distinguishes a settled policy denial from a temporary fail-closed state', () => {
    const denied = { AllowToolCalling: false }
    expect(workspacePolicyResolvedForScope(null, undefined)).toBe(true)
    expect(workspacePolicyResolvedForScope('workspace-1', undefined)).toBe(false)
    expect(workspacePolicyResolvedForScope('workspace-1', denied, { workspacesLoaded: false })).toBe(false)
    expect(workspacePolicyResolvedForScope('workspace-1', denied, { policyLoading: true })).toBe(false)
    expect(workspacePolicyResolvedForScope('workspace-1', denied, { switching: true })).toBe(false)
    expect(workspacePolicyResolvedForScope('workspace-1', denied, { policyError: 'offline' })).toBe(false)
    expect(workspacePolicyResolvedForScope('workspace-1', denied)).toBe(true)
  })

  it('fingerprints only workspace fields that reshape the model catalog', () => {
    const first = workspaceModelPolicyKey('workspace-1', {
      AllowedModelIDs: ['model-b', 'model-a'],
      AllowDrawing: true,
      AllowMCP: false,
    })
    const reordered = workspaceModelPolicyKey('workspace-1', {
      AllowedModelIDs: ['model-a', 'model-b'],
      AllowDrawing: true,
      AllowMCP: true,
    })
    expect(first).toBe(reordered)
    expect(workspaceModelPolicyKey('workspace-1', {
      AllowedModelIDs: ['model-a'],
      AllowDrawing: true,
    })).not.toBe(first)
    expect(workspaceModelPolicyKey('workspace-1', {
      AllowedModelIDs: ['model-a', 'model-b'],
      AllowDrawing: false,
    })).not.toBe(first)
    expect(workspaceModelPolicyKey(null, undefined)).toBe('personal')
  })
})
