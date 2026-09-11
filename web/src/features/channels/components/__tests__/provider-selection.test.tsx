/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { expect, test, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import { ChannelProviderPicker } from '../drawers/channel-provider-picker'

const plugins = [
  {
    key: 'provider-video',
    name: 'OpenAI',
    icon: 'text:PV',
    description: {
      en: 'Video generation via the vendor API',
      zh: '通过厂商接口生成视频',
    },
    models: ['video-model'],
  },
  {
    key: 'another-video',
    name: 'Video Provider',
    icon: 'text:VP',
    models: ['video-one', 'video-two'],
  },
]

test('custom providers have their own tab and keep the search when switching categories', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  const tabs = screen.getByRole('tablist', { name: 'Provider source' })
  expect(within(tabs).getAllByRole('tab')).toHaveLength(4)
  await user.click(within(tabs).getByRole('tab', { name: 'Custom' }))
  const panel = screen.getByRole('tabpanel', { name: 'Custom' })
  expect(within(panel).getAllByRole('option')).toHaveLength(2)
  expect(
    within(panel).getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
  expect(
    within(panel).getByRole('option', { name: 'Custom Built-in #8' })
  ).toBeVisible()
  expect(
    within(panel).queryByRole('option', { name: /Plugin/ })
  ).not.toBeInTheDocument()

  await user.click(within(tabs).getByRole('tab', { name: 'Built-in' }))
  expect(
    screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: 'Custom Built-in #8' })
  ).not.toBeInTheDocument()
  await user.type(screen.getByRole('combobox'), '58')
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
  await user.click(within(tabs).getByRole('tab', { name: 'Custom' }))
  expect(screen.getByRole('combobox')).toHaveValue('58')
  expect(screen.getAllByRole('option')).toHaveLength(1)
  expect(
    screen.getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
})

test('category tabs use arrow and Enter navigation without moving focus into the search field', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      canBindPlugin
      loading={false}
      failed
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  const all = screen.getByRole('tab', { name: 'All' })
  const builtin = screen.getByRole('tab', { name: 'Built-in' })
  await user.click(all)
  await user.keyboard('{ArrowRight}')
  expect(builtin).toHaveFocus()
  expect(all).toHaveAttribute('aria-selected', 'true')
  await user.keyboard('{Enter}')
  expect(builtin).toHaveAttribute('aria-selected', 'true')
  expect(builtin).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Built-in' })).toHaveAttribute(
    'id',
    builtin.getAttribute('aria-controls')
  )
  expect(screen.queryByText('Failed to load plugins')).not.toBeInTheDocument()

  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Plugins' })).toHaveFocus()
  expect(screen.getByText('Failed to load plugins')).toBeVisible()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Custom' })).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Custom' })).toBeVisible()
  expect(screen.queryByText('Failed to load plugins')).not.toBeInTheDocument()
})

test('removing plugin binding permission returns an active plugin tab to all providers', async () => {
  const user = userEvent.setup()
  const props = {
    plugins,
    loading: false,
    failed: false,
    disabled: false,
    onRetry: vi.fn(),
    onSelect: vi.fn(),
  }
  const view = render(<ChannelProviderPicker {...props} canBindPlugin />)
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  view.rerender(<ChannelProviderPicker {...props} canBindPlugin={false} />)
  expect(screen.queryByRole('tab', { name: 'Plugins' })).not.toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'All' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('tabpanel', { name: 'All' })).toBeVisible()
  expect(
    screen.queryByRole('option', { name: /Plugin/ })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
})

test('every built-in provider has a description and Anthropic includes compatible services', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zhCN: zh },
  })
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        plugins={[]}
        canBindPlugin={false}
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={vi.fn()}
      />
    </I18nextProvider>
  )

  for (const option of screen.getAllByRole('option')) {
    expect(option).toHaveAccessibleDescription()
  }
  const anthropic = screen.getByRole('option', {
    name: 'Anthropic Built-in #14',
  })
  expect(anthropic).toHaveAccessibleDescription(
    'Connect to the Anthropic API or compatible services'
  )
  await act(() => language.changeLanguage('zhCN'))
  expect(anthropic).toHaveAccessibleDescription(
    '接入 Anthropic API 或兼容其接口的服务'
  )
  expect(
    within(anthropic).getByText('接入 Anthropic API 或兼容其接口的服务')
  ).toBeVisible()
})

test('deprecated and flexible integration badges preserve provider selection and expose the full explanation', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  const props = {
    plugins: [],
    canBindPlugin: false,
    loading: false,
    failed: false,
    disabled: false,
    onRetry: vi.fn(),
    onSelect: select,
  }
  const view = render(<ChannelProviderPicker {...props} />)
  const custom = screen.getByRole('option', { name: 'Custom Built-in #8' })
  const advanced = screen.getByRole('option', {
    name: 'Advanced Custom Built-in #58',
  })
  expect(custom).toHaveAccessibleDescription(
    'Deprecated · Legacy full-URL integration; use Advanced Custom for new channels'
  )
  await user.click(within(custom).getByText('Deprecated'))
  expect(select).toHaveBeenNthCalledWith(1, { kind: 'builtin', type: 8 })

  const details =
    "New API's flexible channel lets you configure upstream addresses and authentication per endpoint, choose native forwarding or supported protocol conversions, and configure model listing and balance queries independently"
  expect(advanced).toHaveAccessibleDescription(
    `Flexible integration · ${details}`
  )
  expect(
    within(advanced).getByText(
      'Configure endpoint routing, authentication and protocol conversion for different upstream services'
    )
  ).toHaveAttribute('title', details)
  await user.click(within(advanced).getByText('Flexible integration'))
  expect(select).toHaveBeenNthCalledWith(2, { kind: 'builtin', type: 58 })

  view.rerender(<ChannelProviderPicker {...props} disabled />)
  expect(advanced).toHaveAttribute('aria-disabled', 'true')
  await user.click(within(advanced).getByText('Flexible integration'))
  expect(select).toHaveBeenCalledTimes(2)
})

test('plugin descriptions follow the current language with an English fallback and remain selectable', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
  })
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        plugins={plugins}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    </I18nextProvider>
  )

  const option = screen.getByRole('option', {
    name: 'OpenAI Plugin provider-video',
  })
  expect(
    within(option).getByText('Video generation via the vendor API')
  ).toBeVisible()
  expect(option).toHaveAccessibleDescription(
    'Video generation via the vendor API'
  )
  expect(
    screen.getByRole('option', { name: 'Video Provider Plugin another-video' })
  ).not.toHaveAccessibleDescription()

  await act(() => language.changeLanguage('zhCN'))
  expect(within(option).getByText('通过厂商接口生成视频')).toBeVisible()
  expect(option).toHaveAccessibleDescription('通过厂商接口生成视频')
  await act(() => language.changeLanguage('fr'))
  await user.click(
    within(option).getByText('Video generation via the vendor API')
  )
  expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: 'provider-video' })
})

test('built-in and plugin providers with the same name remain distinct and plugin search selects the binding', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      currentProvider={{ kind: 'builtin', type: 1 }}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  expect(screen.getAllByRole('option', { name: /^OpenAI / })).toHaveLength(2)
  expect(
    screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  ).toHaveAttribute('aria-current', 'true')
  expect(
    screen.getByRole('option', { name: 'OpenAI Plugin provider-video' })
  ).not.toHaveAttribute('aria-current')
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(screen.getAllByRole('option')).toHaveLength(2)
  await user.type(screen.getByRole('combobox'), 'provider-video')
  expect(screen.getAllByRole('option')).toHaveLength(1)
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: 'provider-video' })
})

test('searching a known type number selects that type and an unknown positive number remains usable', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  const search = screen.getByRole('combobox')
  await user.type(search, '43')
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 43 })
  await user.clear(search)
  await user.type(search, '999')
  await user.click(screen.getByRole('tab', { name: 'Built-in' }))
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Custom' }))
  await user.click(search)
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 999 })
  await user.clear(search)
  await user.type(search, '61')
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
})

test('an empty plugin filter reports that no plugins can be bound', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(screen.getByText('No plugins available for binding')).toBeVisible()
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
})
