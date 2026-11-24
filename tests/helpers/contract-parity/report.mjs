import fs from 'node:fs'
import path from 'node:path'

/**
 * Read the read-only parity output snapshot produced once by contracts-gate.sh.
 *
 * This helper only transports stdout/stderr/status. It does not parse contracts or
 * implementation code, so parity and reachability keep their independent parsers.
 */
export function readParityReport() {
  const directory = process.env.SFORUM_PARITY_REPORT?.trim()
  if (!directory) return null
  const statusPath = path.join(directory, 'status')
  const stdoutPath = path.join(directory, 'stdout.log')
  const stderrPath = path.join(directory, 'stderr.log')
  for (const file of [statusPath, stdoutPath, stderrPath]) {
    if (!fs.existsSync(file)) {
      throw new Error(`SFORUM_PARITY_REPORT is incomplete; missing ${file}`)
    }
  }
  const status = Number.parseInt(fs.readFileSync(statusPath, 'utf8').trim(), 10)
  if (!Number.isInteger(status)) {
    throw new Error(`SFORUM_PARITY_REPORT has an invalid status file: ${statusPath}`)
  }
  return {
    status,
    stdout: fs.readFileSync(stdoutPath, 'utf8'),
    stderr: fs.readFileSync(stderrPath, 'utf8'),
  }
}
