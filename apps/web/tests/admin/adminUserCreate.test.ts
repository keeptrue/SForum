import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  buildAdminCreateUserPayload,
  validateAdminCreateUserForm,
  type AdminCreateUserForm
} from '../../app/utils/admin/adminUserCreate'

const baseForm: AdminCreateUserForm = {
  username: '  alice  ',
  email: '  alice@example.com ',
  displayName: '  ',
  mode: 'manual',
  password: 'correct horse battery staple',
  roleKeys: ['member'],
  emailVerified: false
}

describe('admin create user payload', () => {
  test('sends a manual password and trims identity fields', () => {
    const payload = buildAdminCreateUserPayload({
      ...baseForm,
      roleKeys: ['moderator', ' member ', 'member']
    })
    expect(payload).toEqual({
      username: 'alice',
      email: 'alice@example.com',
      password: 'correct horse battery staple',
      generatePassword: false,
      roleKeys: ['member', 'moderator'],
      emailVerified: false
    })
  })

  test('omits the password entirely in random generation mode', () => {
    const payload = buildAdminCreateUserPayload({
      ...baseForm,
      mode: 'generated',
      password: 'ignored value',
      displayName: 'Alice',
      emailVerified: true
    })
    expect(payload.password).toBeUndefined()
    expect(payload.generatePassword).toBe(true)
    expect(payload.displayName).toBe('Alice')
    expect(payload.emailVerified).toBe(true)
  })
})

describe('admin create user validation', () => {
  test('requires username, email, password and at least one role', () => {
    const errors = validateAdminCreateUserForm({
      ...baseForm,
      username: '   ',
      email: '',
      password: '',
      roleKeys: ['  ']
    })
    expect(errors).toEqual({
      username: 'required',
      email: 'required',
      password: 'required',
      roles: 'required'
    })
  })

  test('rejects malformed email but accepts generated mode without a password', () => {
    expect(validateAdminCreateUserForm({ ...baseForm, email: 'not-an-email' }).email).toBe('invalid')
    expect(validateAdminCreateUserForm({
      ...baseForm,
      mode: 'generated',
      password: ''
    })).toEqual({})
  })
})

describe('admin create user i18n', () => {
  test('keeps zh-CN and en-US keys in sync', () => {
    const zh = JSON.parse(readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8'))
    const en = JSON.parse(readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8'))
    const required = [
      'createAction', 'createTitle', 'createIntro', 'createUsernameLabel', 'createEmailLabel',
      'createDisplayNameLabel', 'createPasswordLabel', 'createPasswordModeManual',
      'createPasswordModeGenerated', 'createPasswordHint', 'createPasswordGeneratedHint',
      'createRolesLabel', 'createRolesHint', 'createRolesRequired', 'createEmailVerifiedLabel',
      'createEmailVerifiedHint', 'createSubmit', 'createSubmitting', 'createCancel',
      'createSuccess', 'createFailed', 'createResultTitle', 'createResultIntro',
      'createResultUsername', 'createResultPassword', 'createCopyPassword', 'createCopied',
      'createCopyFailed', 'createResultPrimary', 'createResultSecondary', 'createResultWarning',
      'createNewBadge', 'createSuperAdminLocked', 'createPasswordRequired',
      'createUsernameRequired', 'createEmailRequired', 'createEmailInvalid'
    ]
    for (const key of required) {
      expect(zh.admin.users[key]).toBeTruthy()
      expect(en.admin.users[key]).toBeTruthy()
    }
  })
})
