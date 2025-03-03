import { fireEvent, render, screen } from '@testing-library/react';
import React, { ComponentProps } from 'react';

import { LogDetailsRow } from './LogDetailsRow';
import { createLogRow } from './__mocks__/logRow';

type Props = ComponentProps<typeof LogDetailsRow>;

const setup = (propOverrides?: Partial<Props>) => {
  const props: Props = {
    parsedValues: [''],
    parsedKeys: [''],
    isLabel: true,
    wrapLogMessage: false,
    getStats: () => null,
    onClickFilterLabel: () => {},
    onClickFilterOutLabel: () => {},
    onClickShowField: () => {},
    onClickHideField: () => {},
    displayedFields: [],
    row: createLogRow(),
    disableActions: false,
  };

  Object.assign(props, propOverrides);

  return render(
    <table>
      <tbody>
        <LogDetailsRow {...props} />
      </tbody>
    </table>
  );
};

jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  reportInteraction: jest.fn(),
}));

describe('LogDetailsRow', () => {
  it('should render parsed key', () => {
    setup({ parsedKeys: ['test key'] });
    expect(screen.getByText('test key')).toBeInTheDocument();
  });
  it('should render parsed value', () => {
    setup({ parsedValues: ['test value'] });
    expect(screen.getByText('test value')).toBeInTheDocument();
  });

  it('should render metrics button', () => {
    setup();
    expect(screen.getAllByRole('button', { name: 'Ad-hoc statistics' })).toHaveLength(1);
  });

  describe('toggleable filters', () => {
    it('should render filter buttons', () => {
      setup();
      expect(screen.getAllByRole('button', { name: 'Filter for value in query A' })).toHaveLength(1);
      expect(screen.getAllByRole('button', { name: 'Filter out value in query A' })).toHaveLength(1);
      expect(screen.queryByRole('button', { name: 'Remove filter in query A' })).not.toBeInTheDocument();
    });
    it('should render remove filter button when the filter is active', async () => {
      setup({
        isFilterLabelActive: jest.fn().mockResolvedValue(true),
      });
      expect(await screen.findByRole('button', { name: 'Remove filter in query A' })).toBeInTheDocument();
    });
  });

  describe('if props is not a label', () => {
    it('should render a show toggleFieldButton button', () => {
      setup({ isLabel: false });
      expect(screen.getAllByRole('button', { name: 'Show this field instead of the message' })).toHaveLength(1);
    });
  });

  it('should render stats when stats icon is clicked', () => {
    setup({
      parsedKeys: ['key'],
      parsedValues: ['value'],
      getStats: () => {
        return [
          {
            count: 1,
            proportion: 1 / 2,
            value: 'value',
          },
          {
            count: 1,
            proportion: 1 / 2,
            value: 'another value',
          },
        ];
      },
    });

    expect(screen.queryByTestId('logLabelStats')).not.toBeInTheDocument();
    const adHocStatsButton = screen.getByRole('button', { name: 'Ad-hoc statistics' });
    fireEvent.click(adHocStatsButton);
    expect(screen.getByTestId('logLabelStats')).toBeInTheDocument();
    expect(screen.getByTestId('logLabelStats')).toHaveTextContent('another value');
  });

  describe('copy button', () => {
    it('should be invisible unless mouse is over', () => {
      setup({ parsedValues: ['test value'] });
      // This tests a regression where the button was always visible.
      expect(screen.getByTitle('Copy value to clipboard')).not.toBeVisible();
      // Asserting visibility on mouse-over is currently not possible.
    });
  });
});
