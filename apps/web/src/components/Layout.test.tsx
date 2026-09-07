import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppHeader, TabBar } from './Layout'

// React's act() wants to be told it is running under a test harness.
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function mount(node: React.ReactNode) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => {
    root.render(<MemoryRouter>{node}</MemoryRouter>)
  })
  return { host, root }
}

describe('the header', () => {
  let host: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;({ host, root } = mount(<AppHeader />))
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
  })

  const mark = () => host.querySelector<HTMLButtonElement>('.brand-mark')!
  const logo = () => document.body.querySelector('.brand-flash-logo')

  it('shows the name over the build version', () => {
    expect(host.querySelector('h1')?.textContent).toBe('Bulls and Bears')
    expect(host.querySelector('.brand-version')?.textContent).toBe('v0.0.0')
  })

  it('shows the logo over the app on a double tap', () => {
    act(() => mark().click())
    expect(logo()).toBeNull()
    act(() => mark().click())
    expect(logo()).not.toBeNull()
    expect(document.body.textContent).toContain('Small bets, honest books')
  })

  it('clears the logo when the overlay is tapped', () => {
    act(() => {
      mark().click()
      mark().click()
    })
    expect(logo()).not.toBeNull()
    act(() => (document.body.querySelector('.dev-flash') as HTMLElement).click())
    expect(logo()).toBeNull()
  })

  it('throws the developer badge up on one tap', () => {
    act(() => host.querySelector<HTMLButtonElement>('.dev')!.click())
    expect(document.body.querySelector('.dev-flash-logo')).not.toBeNull()
    expect(document.body.textContent).toContain('github.com/chinmay28')
  })
})

describe('the tab bar', () => {
  it('has the four sections in thumb order', () => {
    const { host, root } = mount(<TabBar />)
    const labels = [...host.querySelectorAll('nav a')].map((a) => a.textContent)
    expect(labels).toEqual(['Overview', 'Strategies', 'Book', 'Settings'])
    act(() => root.unmount())
    host.remove()
  })
})
