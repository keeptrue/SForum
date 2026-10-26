export type AdminCreateUserPasswordMode = 'manual' | 'generated'

export type AdminCreateUserForm = {
  username: string
  email: string
  displayName: string
  mode: AdminCreateUserPasswordMode
  password: string
  roleKeys: string[]
  emailVerified: boolean
}

export type AdminCreateUserPayload = {
  username: string
  email: string
  displayName?: string
  password?: string
  generatePassword: boolean
  roleKeys: string[]
  emailVerified: boolean
}

export type AdminCreateUserValidation = {
  username?: 'required'
  email?: 'required' | 'invalid'
  password?: 'required'
  roles?: 'required'
}

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/**
 * 创建用户的请求体：随机密码模式不发送 password，避免服务端把两者都当成
 * 用户意图；角色去重、去空并排序，保持请求稳定。
 */
export function buildAdminCreateUserPayload(form: AdminCreateUserForm): AdminCreateUserPayload {
  const displayName = form.displayName.trim()
  const roleKeys = [...new Set(form.roleKeys.map(key => key.trim()).filter(Boolean))].sort()
  const generatePassword = form.mode === 'generated'

  return {
    username: form.username.trim(),
    email: form.email.trim(),
    ...(displayName ? { displayName } : {}),
    ...(generatePassword ? {} : { password: form.password }),
    generatePassword,
    roleKeys,
    emailVerified: form.emailVerified
  }
}

export function validateAdminCreateUserForm(form: AdminCreateUserForm): AdminCreateUserValidation {
  const errors: AdminCreateUserValidation = {}

  if (!form.username.trim()) {
    errors.username = 'required'
  }
  const email = form.email.trim()
  if (!email) {
    errors.email = 'required'
  } else if (!emailPattern.test(email)) {
    errors.email = 'invalid'
  }
  if (form.mode === 'manual' && !form.password) {
    errors.password = 'required'
  }
  if (form.roleKeys.every(key => !key.trim())) {
    errors.roles = 'required'
  }

  return errors
}
