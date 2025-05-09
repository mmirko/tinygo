//go:build avr && atmega

package machine

import (
	"device/avr"
	"runtime/interrupt"
	"runtime/volatile"
	"unsafe"
)

// I2C on AVR.
type I2C struct {
	srReg *volatile.Register8
	brReg *volatile.Register8
	crReg *volatile.Register8
	drReg *volatile.Register8

	srPS0 byte
	srPS1 byte
	crEN  byte
	crINT byte
	crSTO byte
	crEA  byte
	crSTA byte
}

// I2CConfig is used to store config info for I2C.
type I2CConfig struct {
	Frequency uint32
}

// Configure is intended to setup the I2C interface.
func (i2c *I2C) Configure(config I2CConfig) error {
	// Default I2C bus speed is 100 kHz.
	if config.Frequency == 0 {
		config.Frequency = 100 * KHz
	}

	// Activate internal pullups for twi.
	avr.PORTC.SetBits((avr.DIDR0_ADC4D | avr.DIDR0_ADC5D))

	return i2c.SetBaudRate(config.Frequency)
}

// SetBaudRate sets the communication speed for I2C.
func (i2c *I2C) SetBaudRate(br uint32) error {
	// Initialize twi prescaler and bit rate.
	i2c.srReg.SetBits((i2c.srPS0 | i2c.srPS1))

	// twi bit rate formula from atmega128 manual pg. 204:
	// SCL Frequency = CPU Clock Frequency / (16 + (2 * TWBR))
	// NOTE: TWBR should be 10 or higher for controller mode.
	// It is 72 for a 16mhz board with 100kHz TWI
	i2c.brReg.Set(uint8(((CPUFrequency() / br) - 16) / 2))

	// Enable twi module.
	i2c.crReg.Set(i2c.crEN)

	return nil
}

// Tx does a single I2C transaction at the specified address.
// It clocks out the given address, writes the bytes in w, reads back len(r)
// bytes and stores them in r, and generates a stop condition on the bus.
func (i2c *I2C) Tx(addr uint16, w, r []byte) error {
	if len(w) != 0 {
		i2c.start(uint8(addr), true) // start transmission for writing
		for _, b := range w {
			i2c.writeByte(b)
		}
	}
	if len(r) != 0 {
		i2c.start(uint8(addr), false) // re-start transmission for reading
		for i := range r {            // read each char
			r[i] = i2c.readByte()
		}
	}
	if len(w) != 0 || len(r) != 0 {
		// Stop the transmission after it has been started.
		i2c.stop()
	}
	return nil
}

// start starts an I2C communication session.
func (i2c *I2C) start(address uint8, write bool) {
	// Clear TWI interrupt flag, put start condition on SDA, and enable TWI.
	i2c.crReg.Set((i2c.crINT | i2c.crSTA | i2c.crEN))

	// Wait till start condition is transmitted.
	for !i2c.crReg.HasBits(i2c.crINT) {
	}

	// Write 7-bit shifted peripheral address.
	address <<= 1
	if !write {
		address |= 1 // set read flag
	}
	i2c.writeByte(address)
}

// stop ends an I2C communication session.
func (i2c *I2C) stop() {
	// Send stop condition.
	i2c.crReg.Set(i2c.crEN | i2c.crINT | i2c.crSTO)

	// Wait for stop condition to be executed on bus.
	for !i2c.crReg.HasBits(i2c.crSTO) {
	}
}

// writeByte writes a single byte to the I2C bus.
func (i2c *I2C) writeByte(data byte) error {
	// Write data to register.
	i2c.drReg.Set(data)

	// Clear TWI interrupt flag and enable TWI.
	i2c.crReg.Set(i2c.crEN | i2c.crINT)

	// Wait till data is transmitted.
	for !i2c.crReg.HasBits(i2c.crINT) {
	}
	return nil
}

// readByte reads a single byte from the I2C bus.
func (i2c *I2C) readByte() byte {
	// Clear TWI interrupt flag and enable TWI.
	i2c.crReg.Set(i2c.crEN | i2c.crINT | i2c.crEA)

	// Wait till read request is transmitted.
	for !i2c.crReg.HasBits(i2c.crINT) {
	}

	return byte(i2c.drReg.Get())
}

// Always use UART0 as the serial output.
var DefaultUART = UART0

// UART
var (
	// UART0 is the hardware serial port on the AVR.
	UART0  = &_UART0
	_UART0 = UART{
		Buffer: NewRingBuffer(),

		dataReg:    avr.UDR0,
		baudRegH:   avr.UBRR0H,
		baudRegL:   avr.UBRR0L,
		statusRegA: avr.UCSR0A,
		statusRegB: avr.UCSR0B,
		statusRegC: avr.UCSR0C,
	}
)

func init() {
	// Register the UART interrupt.
	interrupt.New(irq_USART0_RX, _UART0.handleInterrupt)
}

// UART on the AVR.
type UART struct {
	Buffer *RingBuffer

	dataReg  *volatile.Register8
	baudRegH *volatile.Register8
	baudRegL *volatile.Register8

	statusRegA *volatile.Register8
	statusRegB *volatile.Register8
	statusRegC *volatile.Register8
}

// Configure the UART on the AVR. Defaults to 9600 baud on Arduino.
func (uart *UART) Configure(config UARTConfig) {
	if config.BaudRate == 0 {
		config.BaudRate = 9600
	}

	// Prescale formula for u2x mode from AVR MiniCore source code.
	// Same as formula from specification but taking into account rounding error.
	ps := (CPUFrequency()/4/config.BaudRate - 1) / 2
	uart.statusRegA.SetBits(avr.UCSR0A_U2X0)

	// Hardcoded exception for 57600 for compatibility with older bootloaders.
	// Also, prescale cannot be > 4095, so switch back to non-u2x mode if the baud rate is too low.
	if (CPUFrequency() == 16000000 && config.BaudRate == 57600) || ps > 0xfff {
		ps = (CPUFrequency()/8/config.BaudRate - 1) / 2
		uart.statusRegA.ClearBits(avr.UCSR0A_U2X0)
	}

	uart.baudRegH.Set(uint8(ps >> 8))
	uart.baudRegL.Set(uint8(ps & 0xff))

	// enable RX, TX and RX interrupt
	uart.statusRegB.Set(avr.UCSR0B_RXEN0 | avr.UCSR0B_TXEN0 | avr.UCSR0B_RXCIE0)

	// 8-bits data
	uart.statusRegC.Set(avr.UCSR0C_UCSZ01 | avr.UCSR0C_UCSZ00)
}

func (uart *UART) handleInterrupt(intr interrupt.Interrupt) {
	// Read register to clear it.
	data := uart.dataReg.Get()

	// Ensure no error.
	if !uart.statusRegA.HasBits(avr.UCSR0A_FE0 | avr.UCSR0A_DOR0 | avr.UCSR0A_UPE0) {
		// Put data from UDR register into buffer.
		uart.Receive(byte(data))
	}
}

// WriteByte writes a byte of data to the UART.
func (uart *UART) writeByte(c byte) error {
	// Wait until UART buffer is not busy.
	for !uart.statusRegA.HasBits(avr.UCSR0A_UDRE0) {
	}
	uart.dataReg.Set(c) // send char
	return nil
}

func (uart *UART) flush() {}

// SPIConfig is used to store config info for SPI.
type SPIConfig struct {
	Frequency uint32
	LSBFirst  bool
	Mode      uint8
}

// SPI is for the Serial Peripheral Interface
// Data is taken from http://ww1.microchip.com/downloads/en/DeviceDoc/ATmega48A-PA-88A-PA-168A-PA-328-P-DS-DS40002061A.pdf page 169 and following
type SPI struct {
	// The registers for the SPIx port set by the chip
	spcr *volatile.Register8
	spdr *volatile.Register8
	spsr *volatile.Register8

	spcrR0   byte
	spcrR1   byte
	spcrCPHA byte
	spcrCPOL byte
	spcrDORD byte
	spcrSPE  byte
	spcrMSTR byte

	spsrI2X  byte
	spsrSPIF byte

	// The io pins for the SPIx port set by the chip
	sck Pin
	sdi Pin
	sdo Pin
	cs  Pin
}

// Configure is intended to setup the SPI interface.
func (s *SPI) Configure(config SPIConfig) error {

	// This is only here to help catch a bug with the configuration
	// where a machine missed a value.
	if s.spcr == (*volatile.Register8)(unsafe.Pointer(uintptr(0))) ||
		s.spsr == (*volatile.Register8)(unsafe.Pointer(uintptr(0))) ||
		s.spdr == (*volatile.Register8)(unsafe.Pointer(uintptr(0))) ||
		s.sck == 0 || s.sdi == 0 || s.sdo == 0 || s.cs == 0 {
		return errSPIInvalidMachineConfig
	}

	// Make the defaults meaningful
	if config.Frequency == 0 {
		config.Frequency = 4000000
	}

	// Default all port configuration bits to 0 for simplicity
	s.spcr.Set(0)
	s.spsr.Set(0)

	// Setup pins output configuration
	s.sck.Configure(PinConfig{Mode: PinOutput})
	s.sdi.Configure(PinConfig{Mode: PinInput})
	s.sdo.Configure(PinConfig{Mode: PinOutput})

	// Prevent CS glitches if the pin is enabled Low (0, default)
	s.cs.High()
	// If the CS pin is not configured as output the SPI port operates in
	// slave mode.
	s.cs.Configure(PinConfig{Mode: PinOutput})

	frequencyDivider := CPUFrequency() / config.Frequency

	switch {
	case frequencyDivider >= 128:
		s.spcr.SetBits(s.spcrR0 | s.spcrR1)
	case frequencyDivider >= 64:
		s.spcr.SetBits(s.spcrR1)
	case frequencyDivider >= 32:
		s.spcr.SetBits(s.spcrR1)
		s.spsr.SetBits(s.spsrI2X)
	case frequencyDivider >= 16:
		s.spcr.SetBits(s.spcrR0)
	case frequencyDivider >= 8:
		s.spcr.SetBits(s.spcrR0)
		s.spsr.SetBits(s.spsrI2X)
	case frequencyDivider >= 4:
		// The clock is already set to all 0's.
	default: // defaults to fastest which is /2
		s.spsr.SetBits(s.spsrI2X)
	}

	switch config.Mode {
	case Mode1:
		s.spcr.SetBits(s.spcrCPHA)
	case Mode2:
		s.spcr.SetBits(s.spcrCPHA)
	case Mode3:
		s.spcr.SetBits(s.spcrCPHA | s.spcrCPOL)
	default: // default is mode 0
	}

	if config.LSBFirst {
		s.spcr.SetBits(s.spcrDORD)
	}

	// enable SPI, set controller, set clock rate
	s.spcr.SetBits(s.spcrSPE | s.spcrMSTR)

	return nil
}

// Transfer writes the byte into the register and returns the read content
func (s *SPI) Transfer(b byte) (byte, error) {
	s.spdr.Set(uint8(b))

	for !s.spsr.HasBits(s.spsrSPIF) {
	}

	return byte(s.spdr.Get()), nil
}
